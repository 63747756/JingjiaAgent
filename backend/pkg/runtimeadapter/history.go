package runtimeadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/chaitin/MonkeyCode/backend/pkg/tasklog"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

type historyProvider struct {
	ledger       *Ledger
	projectInput func([]byte) []byte
}

func (p *historyProvider) Name() string { return "runtime" }
func (c *Client) ResolveHistory(ctx context.Context, id uuid.UUID) (tasklog.Provider, bool, error) {
	e, err := c.ledger.EnvironmentForTask(ctx, id.String())
	if errors.Is(err, sql.ErrNoRows) {
		// Rejected admissions still have a product task and a durable creation
		// journal, but deliberately have no Sandbox/Run or legacy log stream.
		var owner string
		err = c.ledger.db.QueryRowContext(ctx, `SELECT owner_id::text FROM runtime_creation_attempts WHERE task_id=$1 AND state='failed'`, id).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		if err != nil {
			return nil, true, err
		}
		return &historyProvider{ledger: c.ledger, projectInput: func(data []byte) []byte { return c.publicInputData(owner, data) }}, true, nil
	}
	if err != nil {
		return nil, true, err
	}
	return &historyProvider{ledger: c.ledger, projectInput: func(data []byte) []byte { return c.publicInputData(e.OwnerID, data) }}, true, nil
}

type eventReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readChunks(ctx context.Context, reader eventReader, query string, args ...any) ([]*tasklog.TurnChunk, error) {
	rows, err := reader.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*tasklog.TurnChunk{}
	for rows.Next() {
		var seq uint64
		var turn uint32
		var b []byte
		if err = rows.Scan(&seq, &turn, &b); err != nil {
			return nil, err
		}
		var chunk taskflow.TaskChunk
		if err = json.Unmarshal(b, &chunk); err != nil {
			return nil, err
		}
		out = append(out, &tasklog.TurnChunk{Seq: seq, TurnSeq: turn, Timestamp: chunk.Timestamp, Data: chunk.Data, Event: chunk.Event, Kind: chunk.Kind})
	}
	return out, rows.Err()
}
func snapshot(ctx context.Context, ledger *Ledger, id uuid.UUID) (*tasklog.QueryLatestTurnResp, uint64, error) {
	tx, err := ledger.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var turn int
	var watermark uint64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(max(turn),0),COALESCE(max(seq),0) FROM runtime_events WHERE task_id=$1`, id).Scan(&turn, &watermark); err != nil {
		return nil, 0, err
	}
	chunks, err := readChunks(ctx, tx, `SELECT seq,turn,chunk FROM runtime_events WHERE task_id=$1 AND turn=$2 ORDER BY seq`, id, turn)
	if err != nil {
		return nil, 0, err
	}
	out := &tasklog.QueryLatestTurnResp{Entries: []tasklog.Entry{}}
	for _, chunk := range chunks {
		out.Entries = append(out.Entries, tasklog.Entry{TaskID: id, TS: time.Unix(0, chunk.Timestamp), Event: chunk.Event, Kind: chunk.Kind, TurnSeq: chunk.TurnSeq, Data: string(chunk.Data), MsgSeq: strconv.FormatUint(chunk.Seq, 10)})
	}
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_events WHERE task_id=$1 AND turn<$2)`, id, turn).Scan(&out.HasMore); err != nil {
		return nil, 0, err
	}
	if out.HasMore {
		out.NextCursor = strconv.Itoa(turn)
	}
	return out, watermark, tx.Commit()
}
func (c *Client) ReplayTask(ctx context.Context, id string) (*tasklog.QueryLatestTurnResp, uint64, bool, error) {
	e, err := c.ledger.EnvironmentForTask(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, true, err
	}
	task, err := uuid.Parse(id)
	if err != nil {
		return nil, 0, true, err
	}
	history, seq, err := snapshot(ctx, c.ledger, task)
	if err == nil {
		projectEntries(history, func(data []byte) []byte { return c.publicInputData(e.OwnerID, data) })
	}
	return history, seq, true, err
}
func (c *Client) TaskLiveAfter(ctx context.Context, id string, after uint64, fn func(*taskflow.TaskChunk) error) error {
	e, err := c.ledger.EnvironmentForTask(ctx, id)
	if err != nil {
		return err
	}
	fn = c.publicInputCallback(e.OwnerID, fn)
	timer := time.NewTicker(c.poll)
	defer timer.Stop()
	for {
		chunks, err := c.ledger.Events(ctx, id, int64(after))
		if err != nil {
			return err
		}
		for i := range chunks {
			if err = fn(&chunks[i]); err != nil {
				return err
			}
			after = chunks[i].Seq
		}
		if len(chunks) == 1000 {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func (p *historyProvider) QueryLatestTurn(ctx context.Context, id uuid.UUID, _, _ time.Time) (*tasklog.QueryLatestTurnResp, error) {
	result, _, err := snapshot(ctx, p.ledger, id)
	if err == nil {
		projectEntries(result, p.projectInput)
	}
	return result, err
}

func projectEntries(result *tasklog.QueryLatestTurnResp, project func([]byte) []byte) {
	if result == nil || project == nil {
		return
	}
	for index := range result.Entries {
		if result.Entries[index].Event == "user-input" {
			result.Entries[index].Data = string(project([]byte(result.Entries[index].Data)))
		}
	}
}
func (p *historyProvider) QueryTurns(ctx context.Context, id uuid.UUID, _ time.Time, opts tasklog.QueryTurnsOpts) (*tasklog.QueryTurnsResp, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 2
	}
	limit = min(limit, 10)
	cmp, order := "<", "DESC"
	switch opts.Direction {
	case "", tasklog.DirectionBackward:
	case tasklog.DirectionForward:
		cmp, order = ">", "ASC"
	default:
		return nil, tasklog.ErrDirectionUnsupported
	}
	if opts.Inclusive {
		cmp += "="
	}
	filter := ""
	args := []any{id}
	if opts.Cursor != "" {
		turn, err := strconv.ParseUint(opts.Cursor, 10, 32)
		if err != nil {
			return nil, err
		}
		filter = " AND turn " + cmp + " $2"
		args = append(args, turn)
	}
	args = append(args, limit+1)
	rows, err := p.ledger.db.QueryContext(ctx, fmt.Sprintf(`SELECT DISTINCT turn FROM runtime_events WHERE task_id=$1%s ORDER BY turn %s LIMIT $%d`, filter, order, len(args)), args...)
	if err != nil {
		return nil, err
	}
	turns := []int32{}
	for rows.Next() {
		var turn int32
		if err = rows.Scan(&turn); err != nil {
			rows.Close()
			return nil, err
		}
		turns = append(turns, turn)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	more := len(turns) > limit
	if more {
		turns = turns[:limit]
	}
	out := &tasklog.QueryTurnsResp{Chunks: []*tasklog.TurnChunk{}, HasMore: more}
	if len(turns) == 0 {
		return out, nil
	}
	out.Chunks, err = readChunks(ctx, p.ledger.db, `SELECT seq,turn,chunk FROM runtime_events WHERE task_id=$1 AND turn=ANY($2) ORDER BY turn `+order+`,seq`, id, pq.Array(turns))
	if err != nil {
		return nil, err
	}
	if p.projectInput != nil {
		for _, chunk := range out.Chunks {
			if chunk.Event == "user-input" {
				chunk.Data = p.projectInput(chunk.Data)
			}
		}
	}
	if more {
		out.NextCursor = strconv.Itoa(int(turns[len(turns)-1]))
	}
	return out, nil
}
func (p *historyProvider) QueryUserInputs(ctx context.Context, id uuid.UUID, _ time.Time, cursor string, limit int) (*tasklog.QueryUserInputsResp, error) {
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, 100)
	before := int64(1<<63 - 1)
	if cursor != "" {
		var err error
		before, err = strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return nil, err
		}
	}
	chunks, err := readChunks(ctx, p.ledger.db, `SELECT seq,turn,chunk FROM runtime_events WHERE task_id=$1 AND chunk->>'event'='user-input' AND (chunk->>'timestamp')::bigint<$2 ORDER BY (chunk->>'timestamp')::bigint DESC,seq DESC LIMIT $3`, id, before, limit+1)
	if err != nil {
		return nil, err
	}
	more := len(chunks) > limit
	if more {
		chunks = chunks[:limit]
	}
	out := &tasklog.QueryUserInputsResp{Entries: []*tasklog.UserInputEntry{}, HasMore: more, Authoritative: true}
	for _, chunk := range chunks {
		if p.projectInput != nil {
			chunk.Data = p.projectInput(chunk.Data)
		}
		out.Entries = append(out.Entries, &tasklog.UserInputEntry{Timestamp: chunk.Timestamp, Data: chunk.Data, TurnSeq: chunk.TurnSeq})
	}
	if more {
		out.NextCursor = strconv.FormatInt(chunks[len(chunks)-1].Timestamp, 10)
	}
	return out, nil
}
