package runtimeadapter

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/google/uuid"
)

type Ledger struct {
	db       *sql.DB
	cipher   cipher.AEAD
	capacity config.RuntimeCapacity
}
type Environment struct {
	ID, OwnerID, NodeID, ProjectID, SandboxID, State string
	CreatedAt                                        time.Time
	Request                                          taskflow.CreateVirtualMachineReq
}
type Command struct {
	ID, EnvironmentID, TaskID, Operation, State, RunID, Lease string
	Turn                                                      int
	Offset                                                    int64
	Payload                                                   json.RawMessage
	Submitted                                                 bool
}

func NewLedger(db *sql.DB, key []byte) (*Ledger, error) {
	if db == nil || len(key) != 32 {
		return nil, errors.New("runtime ledger requires database and a 32-byte encryption key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Ledger{db: db, cipher: aead}, nil
}
func LoadKey(file string) ([]byte, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, errors.New("cannot read runtime encryption key file")
	}
	if len(b) != 32 {
		return nil, errors.New("runtime encryption key file must contain exactly 32 raw bytes")
	}
	return b, nil
}
func (s *Ledger) seal(id string, b []byte) ([]byte, error) {
	n := make([]byte, s.cipher.NonceSize())
	if _, err := rand.Read(n); err != nil {
		return nil, err
	}
	return s.cipher.Seal(n, n, b, []byte(id)), nil
}
func (s *Ledger) open(id string, b []byte) ([]byte, error) {
	n := s.cipher.NonceSize()
	if len(b) < n {
		return nil, errors.New("invalid encrypted runtime payload")
	}
	return s.cipher.Open(nil, b[:n], b[n:], []byte(id))
}
func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (s *Ledger) SaveEnvironment(ctx context.Context, e Environment) error {
	return s.saveEnvironment(ctx, e, false)
}
func (s *Ledger) saveEnvironment(ctx context.Context, e Environment, prepare bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.saveEnvironmentTx(ctx, tx, e, prepare); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Ledger) saveEnvironmentTx(ctx context.Context, tx taskflow.SQLExecutor, e Environment, prepare bool) error {
	b, err := json.Marshal(e.Request)
	if err != nil {
		return err
	}
	p, err := s.seal(e.ID, b)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_environments(id,owner_id,node_id,backend,payload) VALUES($1,$2,$3,'agent_compose',$4) ON CONFLICT(id) DO NOTHING`, e.ID, e.OwnerID, e.NodeID, p); err != nil {
		return err
	}
	var owner, node, state string
	var original []byte
	if err = scanSQLRow(ctx, tx, `SELECT owner_id,node_id,state,payload FROM runtime_environments WHERE id=$1 FOR UPDATE`, []any{e.ID}, &owner, &node, &state, &original); err != nil {
		return err
	}
	data, err := s.open(e.ID, original)
	if err != nil {
		return err
	}
	if owner != e.OwnerID || node != e.NodeID || hash(data) != hash(b) {
		return errors.New("runtime environment submission payload conflict")
	}
	if state == "deleted" || state == "stopping" {
		return errors.New("runtime environment is unavailable")
	}
	if err = s.reserveTx(ctx, tx, e); err != nil {
		return err
	}
	if prepare {
		request := taskflow.CreateTaskReq{VMID: e.ID, CodingAgent: taskflow.CodingAgentOpenCode}
		if err = s.enqueueTx(ctx, tx, e.ID, "", "prepare", 0, mustJSON(request)); err != nil {
			return err
		}
	}
	return nil
}
func (s *Ledger) Environment(ctx context.Context, id string) (Environment, error) {
	var e Environment
	var p []byte
	err := s.db.QueryRowContext(ctx, `SELECT id,owner_id,node_id,project_id,sandbox_id,state,created_at,payload FROM runtime_environments WHERE id=$1`, id).Scan(&e.ID, &e.OwnerID, &e.NodeID, &e.ProjectID, &e.SandboxID, &e.State, &e.CreatedAt, &p)
	if err != nil {
		return e, err
	}
	b, err := s.open(e.ID, p)
	if err != nil {
		return e, err
	}
	err = json.Unmarshal(b, &e.Request)
	return e, err
}
func (s *Ledger) EnvironmentForTask(ctx context.Context, task string) (Environment, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT environment_id FROM runtime_task_intents WHERE task_id=$1`, task).Scan(&id)
	if err != nil {
		return Environment{}, err
	}
	return s.Environment(ctx, id)
}
func (s *Ledger) SetEnvironment(ctx context.Context, id, project, sandbox, state string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE runtime_environments SET project_id=$2,sandbox_id=$3,state=CASE WHEN state='stopping' AND $4<>'deleted' THEN state ELSE $4 END WHERE id=$1 AND state<>'deleted'`, id, project, sandbox, state)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("runtime environment is unavailable")
	}
	return nil
}
func (s *Ledger) Stage(ctx context.Context, req taskflow.CreateTaskReq) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.stageTx(ctx, tx, req); err != nil {
		return err
	}
	return tx.Commit()
}

// stageTx participates in the caller's transaction. A Worker on another
// connection cannot see either the intent or preparation before commit.
func (s *Ledger) stageTx(ctx context.Context, tx taskflow.SQLExecutor, req taskflow.CreateTaskReq) error {
	if err := s.reserveEnvironmentTx(ctx, tx, req.VMID, false); err != nil {
		return err
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	p, err := s.seal(req.ID.String(), b)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_task_intents(task_id,environment_id,payload,payload_hash) VALUES($1,$2,$3,$4) ON CONFLICT(task_id) DO NOTHING`, req.ID, req.VMID, p, hash(b))
	if err != nil {
		return err
	}
	var environment string
	if err = scanSQLRow(ctx, tx, `SELECT environment_id FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, []any{req.ID}, &environment); err != nil {
		return err
	}
	if environment != req.VMID {
		return errors.New("runtime task submission payload conflict")
	}
	// The prepare command retains the immutable admission payload. The intent
	// can change after a model switch, but admission retries must still compare
	// against the original request and must not overwrite the current config.
	if err = s.enqueueTx(ctx, tx, req.VMID, req.ID.String(), "prepare", 0, b); err != nil {
		return err
	}
	return nil
}
func (s *Ledger) Admission(ctx context.Context, id string) (taskflow.CreateTaskReq, error) {
	var req taskflow.CreateTaskReq
	var commandID string
	var payload []byte
	err := s.db.QueryRowContext(ctx, `SELECT c.id,c.payload FROM runtime_task_intents i JOIN runtime_commands c ON c.task_id=i.task_id AND c.environment_id=i.environment_id AND c.operation='prepare' AND c.turn=0 WHERE i.task_id=$1`, id).Scan(&commandID, &payload)
	if err != nil {
		return req, err
	}
	data, err := s.open(commandID, payload)
	if err != nil {
		return req, err
	}
	err = json.Unmarshal(data, &req)
	return req, err
}
func (s *Ledger) Intent(ctx context.Context, id string) (taskflow.CreateTaskReq, error) {
	var req taskflow.CreateTaskReq
	var p []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM runtime_task_intents WHERE task_id=$1`, id).Scan(&p)
	if err != nil {
		return req, err
	}
	b, err := s.open(id, p)
	if err != nil {
		return req, err
	}
	err = json.Unmarshal(b, &req)
	return req, err
}
func (s *Ledger) enqueueTx(ctx context.Context, tx taskflow.SQLExecutor, env, task, op string, turn int, b []byte) error {
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(task+":"+env+":"+op+fmt.Sprint(turn))).String()
	p, err := s.seal(id, b)
	if err != nil {
		return err
	}
	var taskID any
	if task != "" {
		taskID = task
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_commands(id,environment_id,task_id,operation,turn,payload,payload_hash) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO NOTHING`, id, env, taskID, op, turn, p, hash(b))
	if err != nil {
		return err
	}
	var digest string
	if err = scanSQLRow(ctx, tx, `SELECT payload_hash FROM runtime_commands WHERE id=$1`, []any{id}, &digest); err != nil {
		return err
	}
	if digest != hash(b) {
		return errors.New("runtime command payload conflict")
	}
	if op == "task" {
		var input taskflow.CreateTaskReq
		if err = json.Unmarshal(b, &input); err != nil {
			return err
		}
		// Acceptance and its public echo commit together, before any Guest work.
		chunk := taskflow.TaskChunk{Event: "user-input", Timestamp: time.Now().UnixNano(), Data: mustJSON(map[string]any{
			"content": []byte(input.Text), "attachments": input.Attachments, "client_message_id": input.ClientMessageID,
		})}
		if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_events(task_id,command_id,source_key,turn,chunk) VALUES($1,$2,'user-input',$3,$4) ON CONFLICT(command_id,source_key) DO NOTHING`, task, id, turn, string(mustJSON(chunk))); err != nil {
			return err
		}
	}
	return nil
}

// Ent transactions expose QueryContext, while QueryRowContext is available on
// database/sql transactions only. Close rows before issuing the next statement.
func scanSQLRow(ctx context.Context, tx taskflow.SQLExecutor, query string, args []any, dest ...any) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err = rows.Scan(dest...); err != nil {
		return err
	}
	return rows.Close()
}
func (s *Ledger) Enqueue(ctx context.Context, env, task, op string, turn int, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.enqueueTx(ctx, tx, env, task, op, turn, b); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Ledger) Claim(ctx context.Context) (Command, error) {
	var c Command
	var p []byte
	lease := uuid.NewString()
	err := s.db.QueryRowContext(ctx, `UPDATE runtime_commands SET lease_token=$1,lease_until=now()+interval '90 seconds',attempts=attempts+1,updated_at=now() WHERE id=(SELECT id FROM runtime_commands WHERE state IN ('pending','submitting','unknown','running') AND available_at<=now() AND (lease_until IS NULL OR lease_until<now()) ORDER BY available_at,created_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING id,environment_id,COALESCE(task_id::text,''),operation,turn,state,run_id,event_offset,payload,submission_started`, lease).Scan(&c.ID, &c.EnvironmentID, &c.TaskID, &c.Operation, &c.Turn, &c.State, &c.RunID, &c.Offset, &p, &c.Submitted)
	if err != nil {
		return c, err
	}
	c.Lease = lease
	c.Payload, err = s.open(c.ID, p)
	return c, err
}
func (s *Ledger) UpdateCommand(ctx context.Context, c Command, state, run string, offset int64) error {
	r, err := s.db.ExecContext(ctx, `UPDATE runtime_commands SET state=$3,run_id=$4,event_offset=$5,lease_token=NULL,lease_until=NULL,available_at=now()+CASE WHEN $3='unknown' THEN LEAST(60,POWER(2,LEAST(attempts,6))) WHEN $3='running' THEN 0.1 ELSE 1 END * interval '1 second',updated_at=now() WHERE id=$1 AND lease_token=$2 AND lease_until>now()`, c.ID, c.Lease, state, run, offset)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("runtime worker lease lost")
	}
	return nil
}
func (s *Ledger) Append(ctx context.Context, c Command, key string, chunk taskflow.TaskChunk) error {
	if c.TaskID == "" {
		return nil
	}
	if chunk.Timestamp == 0 {
		chunk.Timestamp = time.Now().UnixNano()
	}
	b, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockCommand(ctx, tx, c); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_events(task_id,command_id,source_key,turn,chunk) VALUES($1,$2,$3,$4,$5) ON CONFLICT(command_id,source_key) DO NOTHING`, c.TaskID, c.ID, key, c.Turn, string(b)); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Ledger) Events(ctx context.Context, task string, after int64) ([]taskflow.TaskChunk, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq,chunk FROM runtime_events WHERE task_id=$1 AND seq>$2 ORDER BY seq LIMIT 1000`, task, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []taskflow.TaskChunk{}
	for rows.Next() {
		var seq uint64
		var b []byte
		if err = rows.Scan(&seq, &b); err != nil {
			return nil, err
		}
		var chunk taskflow.TaskChunk
		if err = json.Unmarshal(b, &chunk); err != nil {
			return nil, err
		}
		chunk.Seq = seq
		out = append(out, chunk)
	}
	return out, rows.Err()
}
func (s *Ledger) ActiveCommands(ctx context.Context, task string) ([]Command, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,environment_id,operation,turn,state,run_id FROM runtime_commands WHERE task_id=$1 AND state NOT IN ('complete','failed','canceled') ORDER BY turn`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Command{}
	for rows.Next() {
		var c Command
		c.TaskID = task
		if err = rows.Scan(&c.ID, &c.EnvironmentID, &c.Operation, &c.Turn, &c.State, &c.RunID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
