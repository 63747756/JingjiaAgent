package runtimeadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/errcode"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
)

var cpuDecimal = regexp.MustCompile(`^[0-9]{1,8}(\.[0-9]{1,3})?$`)

// Integer arithmetic keeps admission and Docker's cgroup limits consistent.
func requestedResources(r taskflow.CreateVirtualMachineReq) (cpu, memory int64, err error) {
	value := r.Cores
	if value == "" {
		value = "2"
	}
	if !cpuDecimal.MatchString(value) {
		return 0, 0, errcode.ErrRuntimeResources
	}
	parts := strings.Split(value, ".")
	whole, _ := strconv.ParseInt(parts[0], 10, 64)
	cpu = whole * 1000
	if len(parts) == 2 {
		fraction, _ := strconv.ParseInt(parts[1]+strings.Repeat("0", 3-len(parts[1])), 10, 64)
		cpu += fraction
	}
	if cpu <= 0 || cpu > 65536*1000 || r.Memory > math.MaxInt64 {
		return 0, 0, errcode.ErrRuntimeResources
	}
	memory = int64(r.Memory)
	if memory == 0 {
		memory = 8 << 30
	}
	return cpu, memory, nil
}

func capacityLimits(s NodeSnapshot, p config.RuntimeCapacity) (int64, int64) {
	cpu, memory := int64(s.Cores)*1000-p.ReserveCPUMillis, int64(s.Memory)-p.ReserveMemoryBytes
	if cpu < 0 {
		cpu = 0
	}
	if memory < 0 {
		memory = 0
	}
	if p.MaxCPUMillis > 0 && p.MaxCPUMillis < cpu {
		cpu = p.MaxCPUMillis
	}
	if p.MaxMemoryBytes > 0 && p.MaxMemoryBytes < memory {
		memory = p.MaxMemoryBytes
	}
	return cpu, memory
}

func (l *Ledger) refreshCapacity(ctx context.Context, node string, sample NodeSnapshot) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(707326030)`); err != nil {
		return err
	}
	var identity string
	if err = tx.QueryRowContext(ctx, `SELECT capacity_id FROM runtime_nodes WHERE node_id=$1 FOR SHARE`, node).Scan(&identity); err != nil {
		return err
	}
	if identity != sample.CapacityID {
		return errcode.ErrRuntimeCapacityUnavailable
	}
	if l.capacity.Enabled {
		var unknown bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_nodes n WHERE n.capacity_id='' AND n.instance_id IS NOT NULL
   AND n.ready) OR EXISTS(SELECT 1 FROM runtime_environments e LEFT JOIN runtime_nodes n ON n.node_id=e.node_id
   WHERE e.state<>'deleted' AND COALESCE(n.capacity_id,'')='')`).Scan(&unknown); err != nil {
			return err
		}
		if unknown {
			return errcode.ErrRuntimeCapacityUnavailable
		}
	}
	if err = l.syncCapacityTx(ctx, tx, sample); err != nil {
		return err
	}
	return tx.Commit()
}

// Called under the node row lock. Pool limits only become more conservative
// through heartbeat; a stale replica cannot increase or disable a saved policy.
func (l *Ledger) syncCapacityTx(ctx context.Context, tx *sql.Tx, s NodeSnapshot) error {
	if s.CapacityID == "" {
		var enforced bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_capacity_pools WHERE enforced)`).Scan(&enforced); err != nil {
			return err
		}
		if l.capacity.Enabled || enforced {
			return errcode.ErrRuntimeCapacityUnavailable
		}
		return nil
	}
	cpu, memory := capacityLimits(s, l.capacity)
	_, err := tx.ExecContext(ctx, `INSERT INTO runtime_capacity_pools(id,cpu_total_millis,memory_total_bytes,cpu_limit_millis,memory_limit_bytes,enforced)
 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO NOTHING`, s.CapacityID, int64(s.Cores)*1000, int64(s.Memory), cpu, memory, l.capacity.Enabled)
	if err != nil {
		return err
	}
	var enforced bool
	if err = tx.QueryRowContext(ctx, `SELECT enforced FROM runtime_capacity_pools WHERE id=$1 FOR UPDATE`, s.CapacityID).Scan(&enforced); err != nil {
		return err
	}
	// Disabled replicas still observe reductions in actual machine capacity.
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_capacity_pools SET cpu_total_millis=$2,memory_total_bytes=$3,
 cpu_limit_millis=CASE WHEN $6 OR NOT enforced THEN LEAST(cpu_limit_millis,$4) ELSE LEAST(cpu_limit_millis,$2) END,
 memory_limit_bytes=CASE WHEN $6 OR NOT enforced THEN LEAST(memory_limit_bytes,$5) ELSE LEAST(memory_limit_bytes,$3) END,
 enforced=enforced OR $6,updated_at=now() WHERE id=$1`, s.CapacityID, int64(s.Cores)*1000, int64(s.Memory), cpu, memory, l.capacity.Enabled); err != nil {
		return err
	}
	if !enforced && !l.capacity.Enabled {
		return nil
	}
	// Every enrolled alias of this Docker engine belongs to the same pool.
	// Existing environments are counted even when enabling would overfill it.
	rows, err := tx.QueryContext(ctx, `SELECT e.id,e.payload FROM runtime_environments e JOIN runtime_nodes n ON n.node_id=e.node_id
 LEFT JOIN runtime_reservations r ON r.environment_id=e.id
 WHERE n.capacity_id=$1 AND e.state<>'deleted' AND r.environment_id IS NULL`, s.CapacityID)
	if err != nil {
		return err
	}
	type reservation struct {
		id          string
		cpu, memory int64
	}
	pending := []reservation{}
	for rows.Next() {
		var id string
		var payload []byte
		if err = rows.Scan(&id, &payload); err != nil {
			break
		}
		data, openErr := l.open(id, payload)
		if openErr != nil {
			err = openErr
			break
		}
		var request taskflow.CreateVirtualMachineReq
		if err = json.Unmarshal(data, &request); err != nil {
			break
		}
		c, m, resourceErr := requestedResources(request)
		if resourceErr != nil {
			err = resourceErr
			break
		}
		pending = append(pending, reservation{id, c, m})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range pending {
		if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_reservations(environment_id,capacity_id,cpu_millis,memory_bytes) VALUES($1,$2,$3,$4) ON CONFLICT(environment_id) DO NOTHING`, r.id, s.CapacityID, r.cpu, r.memory); err != nil {
			return err
		}
	}
	return nil
}

// Environment -> node (share) -> pool is the admission lock order. Enrollment
// uses node -> pool and reads environment payloads without acquiring row locks.
func (l *Ledger) reserveTx(ctx context.Context, tx taskflow.SQLExecutor, e Environment) error {
	// Shared gate permits parallel admissions, but fences node mapping changes
	// and policy activation/backfill so no uncounted legacy admission can race it.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock_shared(707326030)`); err != nil {
		return err
	}
	cpu, memory, err := requestedResources(e.Request)
	if err != nil {
		return err
	}
	var pool string
	err = scanSQLRow(ctx, tx, `SELECT capacity_id FROM runtime_nodes WHERE node_id=$1 FOR SHARE`, []any{e.NodeID}, &pool)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if pool == "" {
		var enforced bool
		if err = scanSQLRow(ctx, tx, `SELECT EXISTS(SELECT 1 FROM runtime_capacity_pools WHERE enforced)`, nil, &enforced); err != nil {
			return err
		}
		if l.capacity.Enabled || enforced {
			return errcode.ErrRuntimeCapacityUnavailable
		}
		return nil
	}
	var limitCPU, limitMemory int64
	var enforced bool
	if err = scanSQLRow(ctx, tx, `SELECT cpu_limit_millis,memory_limit_bytes,enforced FROM runtime_capacity_pools WHERE id=$1 FOR UPDATE`, []any{pool}, &limitCPU, &limitMemory, &enforced); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errcode.ErrRuntimeCapacityUnavailable
		}
		return err
	}
	if !enforced && !l.capacity.Enabled {
		return nil
	}
	if !enforced {
		return errcode.ErrRuntimeCapacityUnavailable
	}
	var savedPool string
	var savedCPU, savedMemory int64
	var active bool
	err = scanSQLRow(ctx, tx, `SELECT capacity_id,cpu_millis,memory_bytes,active FROM runtime_reservations WHERE environment_id=$1`, []any{e.ID}, &savedPool, &savedCPU, &savedMemory, &active)
	if err == nil {
		if !active || savedPool != pool || savedCPU != cpu || savedMemory != memory {
			return errcode.ErrRuntimeCapacityUnavailable
		}
		return nil // Retry or another prompt round never consumes another reservation.
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var usedCPU, usedMemory int64
	if err = scanSQLRow(ctx, tx, `SELECT COALESCE(SUM(cpu_millis),0),COALESCE(SUM(memory_bytes),0) FROM runtime_reservations WHERE capacity_id=$1 AND active`, []any{pool}, &usedCPU, &usedMemory); err != nil {
		return err
	}
	if cpu > limitCPU || memory > limitMemory || usedCPU > limitCPU-cpu || usedMemory > limitMemory-memory {
		return errcode.ErrRuntimeCapacityExhausted
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_reservations(environment_id,capacity_id,cpu_millis,memory_bytes) VALUES($1,$2,$3,$4)`, e.ID, pool, cpu, memory)
	return err
}

func (l *Ledger) reserveEnvironmentTx(ctx context.Context, tx taskflow.SQLExecutor, id string, allowStopping bool) error {
	var e Environment
	var payload []byte
	if err := scanSQLRow(ctx, tx, `SELECT id,node_id,state,payload FROM runtime_environments WHERE id=$1 FOR UPDATE`, []any{id}, &e.ID, &e.NodeID, &e.State, &payload); err != nil {
		return err
	}
	if e.State == "deleted" || (e.State == "stopping" && !allowStopping) {
		return errors.New("runtime environment is unavailable")
	}
	data, err := l.open(e.ID, payload)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, &e.Request); err != nil {
		return err
	}
	return l.reserveTx(ctx, tx, e)
}

func (l *Ledger) ensureReservation(ctx context.Context, id string) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = l.reserveEnvironmentTx(ctx, tx, id, true); err != nil {
		return err
	}
	return tx.Commit()
}

// Pool lock synchronizes release with concurrent admissions. No TTL release.
func releaseReservationTx(ctx context.Context, tx *sql.Tx, id, reason string) error {
	var pool string
	err := tx.QueryRowContext(ctx, `SELECT capacity_id FROM runtime_reservations WHERE environment_id=$1`, id).Scan(&pool)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT id FROM runtime_capacity_pools WHERE id=$1 FOR UPDATE`, pool).Scan(&pool); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE runtime_reservations SET active=false,release_reason=$2,updated_at=now() WHERE environment_id=$1 AND active`, id, reason)
	return err
}
