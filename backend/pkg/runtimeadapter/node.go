package runtimeadapter

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

const nodeInterval = 10 * time.Second
const nodeFreshness = 35 * time.Second

// Registry preserves the original host ownership/group queries. Configured
// endpoints alone do not grant visibility or permission to create a host.
type HostRegistry interface {
	SyncRuntimeHost(context.Context, config.RuntimeNode, *taskflow.Host) error
	ListRuntimeHosts(context.Context, string, []string) (map[string]*taskflow.Host, error)
}

type NodeSnapshot struct {
	Schema          string  `json:"schema"`
	InstanceID      string  `json:"instance_id"`
	Fingerprint     string  `json:"fingerprint"`
	CapacityID      string  `json:"capacity_id,omitempty"`
	Hostname        string  `json:"hostname"`
	Arch            string  `json:"arch"`
	OS              string  `json:"os"`
	Version         string  `json:"version"`
	Cores           int32   `json:"cores"`
	Memory          uint64  `json:"memory"`
	SampledAt       int64   `json:"sampled_at"`
	MemoryAvailable *uint64 `json:"memory_available_bytes,omitempty"`
	DiskTotal       *uint64 `json:"storage_total_bytes,omitempty"`
	DiskAvailable   *uint64 `json:"storage_available_bytes,omitempty"`
}

func (s NodeSnapshot) validate() error {
	if s.CapacityID != "" {
		b, err := hex.DecodeString(s.CapacityID)
		if err != nil || len(b) != 32 || hex.EncodeToString(b) != s.CapacityID {
			return errors.New("invalid runtime capacity identity")
		}
	}
	id, err := uuid.Parse(s.InstanceID)
	fingerprint, hexErr := hex.DecodeString(s.Fingerprint)
	if err != nil || id == uuid.Nil || id.String() != s.InstanceID || hexErr != nil || len(fingerprint) != 32 || s.Schema != "jingjiaagent.runtime.node.v1" || s.Cores <= 0 || s.Cores > 65536 || s.Memory == 0 || s.Memory > 1<<63-1 || s.OS != "linux" || len(s.Hostname) == 0 || len(s.Hostname) > 256 || len(s.Arch) == 0 || len(s.Arch) > 64 || len(s.Version) > 256 {
		return errors.New("invalid runtime node metadata")
	}
	if s.SampledAt <= 0 || time.Since(time.Unix(s.SampledAt, 0)) > 10*time.Minute || time.Until(time.Unix(s.SampledAt, 0)) > 5*time.Minute {
		return errors.New("invalid runtime node sample time")
	}
	if s.MemoryAvailable != nil && *s.MemoryAvailable > s.Memory {
		return errors.New("invalid runtime node memory capacity")
	}
	if s.DiskTotal != nil && (*s.DiskTotal == 0 || *s.DiskTotal > 1<<63-1) {
		return errors.New("invalid runtime node storage capacity")
	}
	if s.DiskAvailable != nil && (s.DiskTotal == nil || *s.DiskAvailable > *s.DiskTotal) {
		return errors.New("invalid runtime node available storage")
	}
	return nil
}

func (e *Engine) nodeSnapshot(ctx context.Context) (NodeSnapshot, error) {
	var out NodeSnapshot
	if e.nodeHTTP == nil {
		return out, errors.New("runtime node metadata transport is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, observationTimeout)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, e.nodeURL+"/internal/jingjiaagent/node", nil)
	if err != nil {
		return out, errors.New("invalid runtime node metadata request")
	}
	response, err := e.nodeHTTP.Do(r)
	if err != nil {
		return out, errors.New("runtime node metadata query failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return out, errors.New("runtime node metadata query failed")
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(b) > 65536 || json.Unmarshal(b, &out) != nil {
		return out, errors.New("invalid runtime node metadata response")
	}
	return out, out.validate()
}

// Bind before business sync so a reused URL/host ID cannot update metadata for
// another runtime. First attempts are persisted with ready=false.
func (l *Ledger) bindNode(ctx context.Context, id string, s NodeSnapshot) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Mapping changes and admission are serialized without holding RPCs here.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(707326030)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_nodes(node_id) VALUES($1) ON CONFLICT(node_id) DO NOTHING`, id); err != nil {
		return err
	}
	var instance, fingerprint sql.NullString
	var capacity string
	if err = tx.QueryRowContext(ctx, `SELECT instance_id,fingerprint,capacity_id FROM runtime_nodes WHERE node_id=$1 FOR UPDATE`, id).Scan(&instance, &fingerprint, &capacity); err != nil {
		return err
	}
	if capacity != "" && capacity != s.CapacityID {
		return errors.New("recorded runtime capacity identity changed")
	}
	if instance.Valid && (instance.String != s.InstanceID || fingerprint.String != s.Fingerprint) {
		return errors.New("recorded runtime node identity changed")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_nodes SET instance_id=$2,fingerprint=$3,capacity_id=$4 WHERE node_id=$1`, id, s.InstanceID, s.Fingerprint, s.CapacityID); err != nil {
		return errors.New("runtime identity is already assigned to another node")
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// Persist verified engine identity even when the rollout barrier is waiting
	// for another alias to upgrade. Later heartbeats can then complete enrollment.
	return l.refreshCapacity(ctx, id, s)
}
func (l *Ledger) saveNode(ctx context.Context, id string, s NodeSnapshot) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	result, err := l.db.ExecContext(ctx, `UPDATE runtime_nodes SET observation=$4,ready=true,last_seen_at=now(),last_attempt_at=now(),last_error_code='' WHERE node_id=$1 AND instance_id=$2 AND fingerprint=$3`, id, s.InstanceID, s.Fingerprint, b)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("runtime node identity changed during heartbeat")
	}
	return nil
}
func (l *Ledger) failNode(ctx context.Context, id string) error {
	_, err := l.db.ExecContext(ctx, `INSERT INTO runtime_nodes(node_id,last_error_code) VALUES($1,'unavailable') ON CONFLICT(node_id) DO UPDATE SET ready=false,last_attempt_at=now(),last_error_code='unavailable'`, id)
	return err
}
func (l *Ledger) nodeOnline(ctx context.Context, id string) (bool, error) {
	var online bool
	err := l.db.QueryRowContext(ctx, `SELECT COALESCE(ready AND last_seen_at>now()-($2 * interval '1 second'),false) FROM runtime_nodes WHERE node_id=$1`, id, nodeFreshness.Seconds()).Scan(&online)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return online, err
}

func (c *Client) SyncNodes(ctx context.Context) error {
	if c.registry == nil {
		return errors.New("runtime host registry is unavailable")
	}
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(8)
	for id, node := range c.nodes {
		group.Go(func() error {
			err := c.syncNode(ctx, id, node)
			if err != nil {
				c.verifyNode(id, false)
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
				defer cancel()
				if saveErr := c.ledger.failNode(cleanup, id); saveErr != nil {
					return saveErr
				}
				if c.logger != nil {
					c.logger.WarnContext(ctx, "runtime node heartbeat unavailable; host admission disabled", "node_id", id)
				}
			}
			return nil
		})
	}
	return group.Wait()
}
func (c *Client) syncNode(ctx context.Context, id string, node config.RuntimeNode) error {
	n := c.engines[id]
	if n == nil {
		return errors.New("configured runtime node is unavailable")
	}
	s, err := n.nodeSnapshot(ctx)
	if err != nil {
		return err
	}
	if err = c.ledger.bindNode(ctx, id, s); err != nil {
		return err
	}
	info := &taskflow.Host{ID: id, Hostname: s.Hostname, Arch: s.Arch, OS: s.OS, Cores: s.Cores, Memory: s.Memory, Version: s.Version}
	if s.DiskTotal != nil {
		info.Disk = *s.DiskTotal
	}
	if err = c.registry.SyncRuntimeHost(ctx, node, info); err != nil {
		return err
	}
	if err = c.ledger.saveNode(ctx, id, s); err != nil {
		return err
	}
	c.verifyNode(id, true)
	return nil
}
func (c *Client) runNodes(ctx context.Context) error {
	ticker := time.NewTicker(nodeInterval)
	defer ticker.Stop()
	for {
		if err := c.SyncNodes(ctx); err != nil && ctx.Err() == nil && c.logger != nil {
			c.logger.WarnContext(ctx, "runtime node observation persistence failed")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Set during application setup, before any server/Worker starts.
func (c *Client) SetHostRegistry(r HostRegistry) { c.registry = r }

func (c *Client) observationEngine(ctx context.Context, nodeID string) (*Engine, error) {
	if c.registry != nil {
		online, err := c.nodeReady(ctx, nodeID)
		if err != nil {
			return nil, err
		}
		if !online {
			return nil, nil
		}
	}
	return c.engines[nodeID], nil
}

// Shared database health cannot authorize another process's endpoint. Each
// process must independently verify its configured connection against the
// persisted identity, and keep that verification fresh.
func (c *Client) verifyNode(id string, verified bool) {
	c.nodeStateMu.Lock()
	defer c.nodeStateMu.Unlock()
	if c.nodeVerified == nil {
		c.nodeVerified = map[string]time.Time{}
	}
	if verified {
		c.nodeVerified[id] = time.Now()
	} else {
		delete(c.nodeVerified, id)
	}
}
func (c *Client) nodeIsVerified(id string) bool {
	c.nodeStateMu.RLock()
	seen := c.nodeVerified[id]
	c.nodeStateMu.RUnlock()
	return !seen.IsZero() && time.Since(seen) < nodeFreshness
}
func (c *Client) nodeReady(ctx context.Context, id string) (bool, error) {
	if !c.nodeIsVerified(id) {
		return false, nil
	}
	online, err := c.ledger.nodeOnline(ctx, id)
	return online && c.nodeIsVerified(id), err
}
