package runtimeadapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/chaitin/MonkeyCode/backend/pkg/clickhouse"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/lib/pq"
)

// The caller supplies task IDs resolved from the authorized team's members.
// Runtime conversations use the same committed user-input events as Web history,
// including stopped/recycled environments, and never depend on a live Guest.
func (c *Client) QueryTeamConversationStats(ctx context.Context, q clickhouse.TeamConversationQuery) (clickhouse.TeamConversationStats, error) {
	var out clickhouse.TeamConversationStats
	if len(q.TaskIDs) == 0 {
		return out, nil
	}
	err := c.ledger.db.QueryRowContext(ctx, `SELECT count(*),
 count(*) FILTER (WHERE created_at >= $2 AND created_at < $4),
 count(*) FILTER (WHERE created_at >= $3 AND created_at < $4)
 FROM runtime_events WHERE task_id=ANY($1::uuid[]) AND chunk->>'event'='user-input'`,
		pq.Array(q.TaskIDs), q.Start7d, q.TodayStart, q.End).Scan(&out.Total, &out.Count7d, &out.CountToday)
	if err != nil {
		return out, err
	}
	rows, err := c.ledger.db.QueryContext(ctx, `SELECT to_char(created_at AT TIME ZONE $4,'YYYY-MM-DD'),count(*)
 FROM runtime_events WHERE task_id=ANY($1::uuid[]) AND chunk->>'event'='user-input'
 AND created_at >= $2 AND created_at < $3 GROUP BY 1 ORDER BY 1`, pq.Array(q.TaskIDs), q.TrendStart, q.End, q.End.Location().String())
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var day clickhouse.TeamConversationDailyCount
		if err := rows.Scan(&day.Date, &day.Count); err != nil {
			return out, err
		}
		out.DailyCreated = append(out.DailyCreated, day)
	}
	return out, rows.Err()
}

func (c *Client) QueryTeamConversations(ctx context.Context, q clickhouse.TeamConversationListQuery) (*clickhouse.TeamConversationListResult, error) {
	out := &clickhouse.TeamConversationListResult{Rows: []clickhouse.TeamConversationRow{}}
	if len(q.TaskIDs) == 0 {
		return out, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, 100)
	before := int64(1<<63 - 1)
	if q.Cursor != "" {
		value, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil {
			return nil, errors.New("invalid runtime conversation cursor")
		}
		before, err = strconv.ParseInt(string(value), 10, 64)
		if err != nil || before <= 0 {
			return nil, errors.New("invalid runtime conversation cursor")
		}
	}
	rows, err := c.ledger.db.QueryContext(ctx, `SELECT ev.seq,ev.task_id::text,ev.turn,ev.chunk,e.owner_id::text
 FROM runtime_events ev JOIN runtime_task_intents i ON i.task_id=ev.task_id
 JOIN runtime_environments e ON e.id=i.environment_id
 WHERE ev.task_id=ANY($1::uuid[]) AND ev.chunk->>'event'='user-input' AND ev.seq < $2
 ORDER BY ev.seq DESC LIMIT $3`, pq.Array(q.TaskIDs), before, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var seq uint64
		var task, owner string
		var turn uint32
		var raw []byte
		if err := rows.Scan(&seq, &task, &turn, &raw, &owner); err != nil {
			return nil, err
		}
		var chunk taskflow.TaskChunk
		if err := json.Unmarshal(raw, &chunk); err != nil {
			return nil, fmt.Errorf("decode runtime conversation: %w", err)
		}
		out.Rows = append(out.Rows, clickhouse.TeamConversationRow{TaskID: task, TS: time.Unix(0, chunk.Timestamp),
			Event: chunk.Event, Kind: chunk.Kind, TurnSeq: turn,
			Data: string(c.publicInputData(owner, chunk.Data)), MsgSeqStart: seq, MsgSeqEnd: seq})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out.Rows) > limit {
		out.HasNextPage = true
		out.Rows = out.Rows[:limit]
	}
	if out.HasNextPage {
		out.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatUint(out.Rows[len(out.Rows)-1].MsgSeqStart, 10)))
	}
	return out, nil
}
