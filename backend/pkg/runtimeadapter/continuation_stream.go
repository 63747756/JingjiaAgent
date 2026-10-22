package runtimeadapter

import (
	"context"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

// Subscribe to this immutable input, rather than the latest task watermark.
// This returns the original receipt after a lost HTTP/socket response and
// prevents an older round's terminal event from closing a queued input stream.
func (c *Client) TaskInputLive(ctx context.Context, task, message string, fn func(*taskflow.TaskChunk) error) error {
	e, err := c.ledger.EnvironmentForTask(ctx, task)
	if err != nil {
		return err
	}
	var command string
	if err = c.ledger.db.QueryRowContext(ctx, `SELECT command_id FROM runtime_events WHERE task_id=$1 AND chunk->>'event'='user-input' AND convert_from(decode(chunk->>'data','base64'),'UTF8')::jsonb->>'client_message_id'=$2 ORDER BY seq LIMIT 1`, task, message).Scan(&command); err != nil {
		return err
	}
	fn = c.publicInputCallback(e.OwnerID, fn)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var after uint64
	for {
		chunks, err := readChunks(ctx, c.ledger.db, `SELECT seq,turn,chunk FROM runtime_events WHERE task_id=$1 AND command_id=$2 AND seq>$3 ORDER BY seq LIMIT 1000`, task, command, after)
		if err != nil {
			return err
		}
		for _, chunk := range chunks {
			after = chunk.Seq
			if err = fn(&taskflow.TaskChunk{Seq: chunk.Seq, Timestamp: chunk.Timestamp, Event: chunk.Event, Kind: chunk.Kind, Data: chunk.Data}); err != nil {
				return err
			}
			if chunk.Event == "task-ended" {
				return nil
			}
		}
		if len(chunks) == 1000 {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
