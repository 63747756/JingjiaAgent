package workartifact

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/workspacesnapshot"
	"github.com/google/uuid"
)

// Hold a task-scoped session lock from selection through restore registration.
// A persisted runtime_restores row protects the async download after release.
func (s *Service) checkpointLock(ctx context.Context, task uuid.UUID, try bool) (func(), bool, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	key := "work-checkpoint:" + task.String()
	var acquired bool
	if try {
		err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&acquired)
	} else {
		_, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, key)
		acquired = err == nil
	}
	if err != nil || !acquired {
		if err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		conn.Close()
		return nil, false, err
	}
	return func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released bool
		if err := conn.QueryRowContext(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key).Scan(&released); err != nil || !released {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		conn.Close()
	}, true, nil
}

type retainedCheckpoint struct {
	id                 uuid.UUID
	state, key, schema string
	generation         int
	at                 time.Time
}

func (s *Service) PruneCheckpoints(ctx context.Context, task uuid.UUID) error {
	deleter, ok := s.store.(interface {
		DeleteObject(context.Context, string) error
	})
	if !ok {
		return errors.New("对象存储不支持备份清理")
	}
	unlock, locked, err := s.checkpointLock(ctx, task, true)
	if err != nil || !locked {
		return err
	}
	defer unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Lock before checking restore pins. A direct runtime restore also takes a
	// shared checkpoint row lock before it registers its async restore.
	if _, err = tx.ExecContext(ctx, `UPDATE workspace_checkpoints SET retention_checked_at=now() WHERE task_id=$1 AND state IN ('committed','pruning')`, task); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT cp.id,cp.state,cp.archive_key,COALESCE(cp.manifest->>'schema',''),cp.generation,COALESCE(cp.committed_at,cp.created_at) FROM workspace_checkpoints cp WHERE cp.task_id=$1 AND cp.state IN ('committed','pruning') ORDER BY COALESCE(cp.committed_at,cp.created_at) DESC,cp.created_at DESC,cp.id DESC`, task)
	if err != nil {
		return err
	}
	var checkpoints []retainedCheckpoint
	for rows.Next() {
		var cp retainedCheckpoint
		if err = rows.Scan(&cp.id, &cp.state, &cp.key, &cp.schema, &cp.generation, &cp.at); err != nil {
			rows.Close()
			return err
		}
		checkpoints = append(checkpoints, cp)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var newest *retainedCheckpoint
	for i := range checkpoints {
		cp := &checkpoints[i]
		if cp.state == "committed" && cp.schema == workspacesnapshot.Schema {
			newest = cp
			break
		}
	}
	var obsolete []retainedCheckpoint
	for _, cp := range checkpoints {
		remove := cp.state == "pruning"
		if newest != nil && cp.id != newest.id {
			remove = remove || cp.schema == workspacesnapshot.Schema || ((cp.schema == "jingjiaagent.workspace.checkpoint.v1" || cp.schema == "jingjiaagent.workspace.checkpoint.v2") && cp.generation <= newest.generation && !cp.at.After(newest.at))
		}
		if !remove {
			continue
		}
		var pinned bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_restores WHERE checkpoint_id=$1 AND state='preparing') OR EXISTS(SELECT 1 FROM runtime_environments e WHERE (e.checkpoint_id=$1 OR e.archiving_checkpoint_id=$1) AND to_jsonb(e)->>'state' IN ('stopping','checkpointing','restoring'))`, cp.id).Scan(&pinned); err != nil {
			return err
		}
		if pinned {
			continue
		}
		if cp.key != fmt.Sprintf("work-private/checkpoints/%s/%s.zip", task, cp.id) {
			return errors.New("备份清理路径校验失败")
		}
		if _, err = tx.ExecContext(ctx, `UPDATE workspace_checkpoints SET state='pruning' WHERE id=$1`, cp.id); err != nil {
			return err
		}
		obsolete = append(obsolete, cp)
		if len(obsolete) >= 32 {
			break
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	var cleanupErr error
	for _, cp := range obsolete {
		if err = deleter.DeleteObject(ctx, cp.key); err != nil {
			_, _ = s.db.ExecContext(ctx, `UPDATE workspace_checkpoints SET error='旧备份清理失败，等待重试' WHERE id=$1 AND state='pruning'`, cp.id)
			cleanupErr = errors.Join(cleanupErr, err)
			continue
		}
		if _, err = s.db.ExecContext(ctx, `UPDATE workspace_checkpoints SET state='pruned',error='' WHERE id=$1 AND state='pruning'`, cp.id); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	// Derived spreadsheet restore packages are transient transport objects.
	// Never delete a package still used by an unfinished async restoration.
	rows, err = s.db.QueryContext(ctx, `SELECT o.archive_key FROM work_restore_objects o JOIN workspace_checkpoints cp ON cp.id=o.checkpoint_id WHERE cp.task_id=$1 AND NOT EXISTS(SELECT 1 FROM runtime_restores r WHERE r.archive_key=o.archive_key AND r.state IN ('preparing','failed'))`, task)
	if err != nil {
		return errors.Join(cleanupErr, err)
	}
	var keys []string
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, key := range keys {
		base := strings.TrimSuffix(path.Base(key), ".zip")
		if _, err = uuid.Parse(base); err != nil || key != "work-private/restores/"+task.String()+"/"+base+".zip" {
			cleanupErr = errors.Join(cleanupErr, errors.New("恢复包清理路径校验失败"))
			continue
		}
		if err = deleter.DeleteObject(ctx, key); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
			continue
		}
		if _, err = s.db.ExecContext(ctx, `DELETE FROM work_restore_objects WHERE archive_key=$1`, key); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

func (s *Service) sweepCheckpoints(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT task_id FROM workspace_checkpoints WHERE state IN ('committed','pruning') GROUP BY task_id HAVING count(*)>1 OR bool_or(state='pruning') OR EXISTS(SELECT 1 FROM work_restore_objects o JOIN workspace_checkpoints cp ON cp.id=o.checkpoint_id WHERE cp.task_id=workspace_checkpoints.task_id) ORDER BY min(retention_checked_at),task_id LIMIT 8`)
	if err != nil {
		return err
	}
	var tasks []uuid.UUID
	for rows.Next() {
		var task uuid.UUID
		if err = rows.Scan(&task); err != nil {
			rows.Close()
			return err
		}
		tasks = append(tasks, task)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var result error
	for _, task := range tasks {
		if err = s.PruneCheckpoints(ctx, task); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

// Keep database rows for foreign keys/audit; only their private archive objects
// are removed. Artifacts, artifact versions, and uploaded resources are untouched.
