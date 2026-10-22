package workartifact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/asseturl"
	"github.com/63747756/jingjiaagent/backend/pkg/workspacesnapshot"
	"github.com/google/uuid"
	"time"
)

func (s *Service) Checkpoints(ctx context.Context, uid, tid uuid.UUID) ([]Checkpoint, error) {
	if err := s.enabled(); err != nil {
		return nil, err
	}
	if _, err := s.taskPermission(ctx, uid, tid); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,environment_id,generation,state,payload_sha256,error,COALESCE(committed_at,created_at),archive_key FROM workspace_checkpoints WHERE task_id=$1 AND state NOT IN ('pruning','pruned') ORDER BY committed_at DESC NULLS LAST,created_at DESC LIMIT 100`, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Checkpoint{}
	for rows.Next() {
		var c Checkpoint
		if err = rows.Scan(&c.ID, &c.TaskID, &c.EnvironmentID, &c.Generation, &c.State, &c.PayloadSHA256, &c.Error, &c.CreatedAt, &c.ArchiveKey); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

var errCheckpointIntegrity = errors.New("保存节点完整性校验失败")

func (s *Service) Restore(ctx context.Context, uid, tid, cid uuid.UUID) (string, error) {
	if err := s.enabled(); err != nil {
		return "", err
	}
	if s.guard != nil {
		if err := s.guard.CheckWorkTaskAccess(ctx, uid, tid, true); err != nil {
			return "", err
		}
	}
	pid, err := s.taskPermission(ctx, uid, tid)
	if err != nil {
		return "", err
	}
	unlock, _, err := s.checkpointLock(ctx, tid, false)
	if err != nil {
		return "", err
	}
	defer unlock()
	if s.runtime == nil {
		return "", errors.New("工作恢复需要远程运行时")
	}
	checkpoints, err := s.Checkpoints(ctx, uid, tid)
	if err != nil {
		return "", err
	}
	var selected *Checkpoint
	for _, cp := range checkpoints {
		if cp.State == "committed" && (cid == uuid.Nil || cp.ID == cid) {
			selected = &cp
			break
		}
	}
	if selected == nil {
		return "", errcode.ErrNotFound
	}
	// Revalidate the actual stored archive, not just the database marker.
	r, err := s.store.GetObject(ctx, selected.ArchiveKey)
	if err != nil {
		return "", err
	}
	f, digest, size, err := spool(r, max(s.cfg.Work.ArchiveLimit(), workspacesnapshot.MaxArchiveBytes))
	r.Close()
	if err != nil {
		return "", err
	}
	defer removeSpool(f)
	if digest != selected.PayloadSHA256 {
		return "", fmt.Errorf("%w：归档摘要不匹配", errCheckpointIntegrity)
	}
	manifest, z, err := verifyArchive(f, size, selected.ID, s.cfg.Work.FileLimit(), s.cfg.Work.ArchiveLimit(), s.cfg.Work.FileCountLimit())
	if err != nil {
		return "", fmt.Errorf("%w：%v", errCheckpointIntegrity, err)
	}
	restoreKey, restoreDigest := selected.ArchiveKey, selected.PayloadSHA256
	derived, derivedDigest, derivedSize, err := s.restoreArchive(ctx, tid, manifest, z)
	if err != nil {
		return "", err
	}
	if derived != nil {
		defer removeSpool(derived)
		restoreKey = fmt.Sprintf("work-private/restores/%s/%s.zip", tid, uuid.New())
		restoreDigest = derivedDigest
		if _, err = s.db.ExecContext(ctx, `INSERT INTO work_restore_objects(archive_key,checkpoint_id) VALUES($1,$2)`, restoreKey, selected.ID); err != nil {
			return "", err
		}
		if err = s.putVerified(ctx, restoreKey, derived, derivedDigest, derivedSize); err != nil {
			return "", err
		}
	}
	var raw []byte
	if err = s.db.QueryRowContext(ctx, `SELECT resource_manifest FROM work_tasks WHERE task_id=$1`, tid).Scan(&raw); err != nil {
		return "", err
	}
	var inputs []InputSnapshot
	if err = json.Unmarshal(raw, &inputs); err != nil {
		return "", err
	}
	ids := []uuid.UUID{}
	for _, input := range inputs {
		ids = append(ids, input.VersionID)
	}
	attachments, _, err := s.ResolveInputs(ctx, uid, pid, ids)
	if err != nil {
		return "", err
	}
	for index := range attachments {
		key, ok := asseturl.Parse(attachments[index].URL)
		if !ok {
			return "", errors.New("恢复资料地址无效")
		}
		attachments[index].URL, err = s.store.PresignGet(ctx, key, 15*time.Minute)
		if err != nil {
			return "", err
		}
	}
	url, err := s.store.PresignGet(ctx, restoreKey, 15*time.Minute)
	if err != nil {
		return "", err
	}
	url += fmt.Sprintf("#jingjiaagent-checkpoint=%s&expected_sha256=%s", selected.ID, restoreDigest)
	return s.runtime.RestoreCheckpoint(ctx, uid, tid, selected.ID, url, restoreKey, restoreDigest, attachments)
}
