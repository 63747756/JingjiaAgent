package runtimeadapter

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

// Fence new inputs while stopping/recycling. If removal fails the task stays
// readable and can be retried; the fence is only permanent for a deleted task.
func (c *Client) PrepareTaskDeletion(ctx context.Context, id string) (func(), error) {
	if _, err := c.ledger.db.ExecContext(ctx, `UPDATE runtime_task_intents SET deletion_requested=true WHERE task_id=$1`, id); err != nil {
		return nil, err
	}
	release := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = c.ledger.db.ExecContext(cleanup, `UPDATE runtime_task_intents SET deletion_requested=false WHERE task_id=$1 AND EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL)`, id)
	}
	tid, err := uuid.Parse(id)
	if err != nil {
		release()
		return nil, err
	}
	if c.ledger.workEnabled {
		if _, err = c.cancelWaitingContinuations(ctx, tid); err != nil {
			release()
			return nil, err
		}
	}
	if _, err = c.StopTaskAndWait(ctx, id); err != nil {
		release()
		return nil, errors.New("任务停止尚未完成，请稍后重试删除: " + err.Error())
	}
	return release, nil
}
