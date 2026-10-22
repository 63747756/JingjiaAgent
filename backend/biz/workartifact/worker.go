package workartifact

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/63747756/jingjiaagent/backend/pkg/workspacesnapshot"
	"github.com/google/uuid"
)

type storageJob struct {
	ID, TaskID    uuid.UUID
	EnvironmentID string
	CommandID     *uuid.UUID
	Lease         uuid.UUID
	Request       []byte
	Attempts      int
	Kind          string
}
type Worker struct {
	s      *Service
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func NewWorker(s *Service) *Worker {
	ctx, cancel := context.WithCancel(context.Background())
	return &Worker{s: s, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}
func (w *Worker) Name() string { return "Work artifact storage and conversion" }
func (w *Worker) Start() error {
	defer close(w.done)
	if !w.s.cfg.Work.Enabled {
		<-w.ctx.Done()
		return nil
	}
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			ctx, cancel := context.WithTimeout(w.ctx, 45*time.Second)
			if err := w.s.sweepCheckpoints(ctx); err != nil && w.ctx.Err() == nil {
				w.s.logger.Warn("old workspace backup cleanup retained for retry")
			}
			cancel()
			select {
			case <-w.ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	defer func() { w.cancel(); <-cleanupDone }()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		if err := w.s.Step(w.ctx); err != nil && !errors.Is(err, sql.ErrNoRows) && w.ctx.Err() == nil {
			w.s.logger.Warn("work storage job retained for retry")
		}
		select {
		case <-w.ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}
func (w *Worker) Stop() error {
	w.cancel()
	select {
	case <-w.done:
		return w.s.db.Close()
	case <-time.After(10 * time.Second):
		return context.DeadlineExceeded
	}
}

func (s *Service) Step(ctx context.Context) error {
	if s.runtime == nil {
		return errors.New("work requires agent-compose runtime")
	}
	var job storageJob
	job.Lease = uuid.New()
	err := s.db.QueryRowContext(ctx, `WITH next AS (SELECT id FROM work_storage_jobs WHERE kind IN ('checkpoint','artifacts') AND available_at<=now() AND ((state IN ('pending','failed') AND attempts<3) OR (state='running' AND lease_until<now())) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE work_storage_jobs j SET state='running',lease_token=$1,lease_until=now()+interval '2 minutes',attempts=attempts+1,updated_at=now() FROM next WHERE j.id=next.id RETURNING j.id,j.task_id,j.environment_id,j.command_id,j.request,j.attempts,j.kind`, job.Lease).Scan(&job.ID, &job.TaskID, &job.EnvironmentID, &job.CommandID, &job.Request, &job.Attempts, &job.Kind)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		tick := time.NewTicker(20 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				r, err := s.db.ExecContext(ctx, `UPDATE work_storage_jobs SET lease_until=now()+interval '2 minutes' WHERE id=$1 AND lease_token=$2 AND state='running'`, job.ID, job.Lease)
				if err != nil {
					cancel()
					return
				}
				n, _ := r.RowsAffected()
				if n != 1 {
					cancel()
					return
				}
			}
		}
	}()
	if job.Attempts > 3 {
		err = errors.New("保存进程多次中断，自动重试已暂停")
	} else {
		err = s.processJob(ctx, job)
	}
	if err != nil {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		if errors.Is(err, taskflow.ErrCheckpointBusy) {
			_, e := s.db.ExecContext(cleanup, `UPDATE work_storage_jobs SET state='pending',attempts=GREATEST(0,attempts-1),lease_token=NULL,lease_until=NULL,available_at=now()+interval '30 seconds',updated_at=now() WHERE id=$1 AND lease_token=$2`, job.ID, job.Lease)
			return e
		}
		message := storageFailureMessage(err)
		if job.Kind == "artifacts" {
			message = strings.ReplaceAll(message, "工作空间保存", "成果保存")
		}
		_, _ = s.db.ExecContext(cleanup, `UPDATE work_storage_jobs SET state='failed',lease_token=NULL,lease_until=NULL,error=$3,available_at=now()+interval '30 seconds',updated_at=now() WHERE id=$1 AND lease_token=$2`, job.ID, job.Lease, message)
		_, _ = s.db.ExecContext(cleanup, `UPDATE workspace_checkpoints SET error=$2 WHERE id=$1 AND state<>'committed'`, job.ID, message)
		if job.Attempts >= 3 {
			if abort, ok := s.runtime.(interface {
				AbortCheckpoint(context.Context, uuid.UUID, uuid.UUID, string) error
			}); ok {
				_ = abort.AbortCheckpoint(cleanup, job.TaskID, job.ID, job.EnvironmentID)
			}
		}
	}
	return err
}

func (s *Service) processJob(ctx context.Context, j storageJob) error {
	uid, err := s.archiveOwner(ctx, j)
	if err != nil {
		return err
	}
	var request struct {
		Scope string `json:"scope"`
	}
	if err = json.Unmarshal(j.Request, &request); err != nil && len(j.Request) > 0 {
		return err
	}
	if request.Scope != "workspace" {
		return s.processArtifacts(ctx, uid, j)
	}
	var committed bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_checkpoints WHERE id=$1 AND state IN ('committed','pruning','pruned'))`, j.ID).Scan(&committed); err != nil {
		return err
	}
	if !committed {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO workspace_checkpoints(id,task_id,environment_id,command_id,generation) SELECT $1,$2,$3,$4,generation FROM runtime_environments WHERE id=$3 ON CONFLICT(id) DO NOTHING`, j.ID, j.TaskID, j.EnvironmentID, j.CommandID); err != nil {
			return err
		}
		r, err := s.runtime.ExportCheckpoint(ctx, j.TaskID, j.ID, j.EnvironmentID)
		if err != nil {
			return err
		}
		f, digest, size, err := spool(r, workspacesnapshot.MaxArchiveBytes)
		r.Close()
		if err != nil {
			return err
		}
		defer removeSpool(f)
		manifest, _, err := verifyArchive(f, size, j.ID, s.cfg.Work.FileLimit(), s.cfg.Work.ArchiveLimit(), s.cfg.Work.FileCountLimit())
		if err != nil {
			return err
		}
		if manifest.Schema != workspacesnapshot.Schema {
			return errors.New("运行节点没有返回完整工作空间，原环境保留")
		}
		key := fmt.Sprintf("work-private/checkpoints/%s/%s.zip", j.TaskID, j.ID)
		if err = s.putVerified(ctx, key, f, digest, size); err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		// Use the same environment -> job lock order as export preparation.
		var environment string
		if err = tx.QueryRowContext(ctx, `SELECT id FROM runtime_environments WHERE id=$1 FOR UPDATE`, j.EnvironmentID).Scan(&environment); err != nil {
			return err
		}
		var lease bool
		if err = tx.QueryRowContext(ctx, `SELECT lease_token=$2 AND lease_until>now() FROM work_storage_jobs WHERE id=$1 FOR UPDATE`, j.ID, j.Lease).Scan(&lease); err != nil {
			return err
		}
		if !lease {
			return errors.New("storage worker lease lost")
		}
		if _, err = tx.ExecContext(ctx, `UPDATE workspace_checkpoints SET state='committed',archive_key=$2,payload_sha256=$3,size=$4,manifest=$5,error='',committed_at=now() WHERE id=$1`, j.ID, key, digest, size, string(jsonBytes(manifest))); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	// Full backup preservation never decides an idle deadline and is independent
	// of artifact extraction/conversion. Retry finalization without re-export.
	if err := s.runtime.CompleteCheckpoint(ctx, j.TaskID, j.ID, j.EnvironmentID); err != nil {
		return err
	}
	return s.finishStorageJob(ctx, j)
}

func (s *Service) finishStorageJob(ctx context.Context, j storageJob) error {
	result, err := s.db.ExecContext(ctx, `UPDATE work_storage_jobs SET state='complete',lease_token=NULL,lease_until=NULL,error='',updated_at=now() WHERE id=$1 AND lease_token=$2`, j.ID, j.Lease)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("storage worker lease lost")
	}
	if err = s.PruneCheckpoints(ctx, j.TaskID); err != nil && s.logger != nil {
		s.logger.Warn("old workspace backup cleanup retained for retry")
	}
	return nil
}

func (s *Service) processArtifacts(ctx context.Context, owner uuid.UUID, j storageJob) error {
	var saved bool
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(request->>'artifacts_saved'='true',false) FROM work_storage_jobs WHERE id=$1`, j.ID).Scan(&saved); err != nil {
		return err
	}
	if !saved {
		stream, err := s.runtime.ExportArtifacts(ctx, j.TaskID, j.ID, j.EnvironmentID)
		if err != nil {
			return err
		}
		err = s.readArtifacts(ctx, owner, j, stream)
		stream.Close()
		if err != nil {
			return err
		}
		result, err := s.db.ExecContext(ctx, `UPDATE work_storage_jobs SET kind='artifacts',request=request || '{"artifacts_saved":true}'::jsonb WHERE id=$1 AND lease_token=$2 AND lease_until>now()`, j.ID, j.Lease)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return errors.New("成果保存租约已失效")
		}
	}
	if err := s.runtime.CompleteCheckpoint(ctx, j.TaskID, j.ID, j.EnvironmentID); err != nil {
		return err
	}
	if err := s.convertTask(ctx, j.TaskID); err != nil {
		return err
	}
	return s.finishStorageJob(ctx, j)
}

func (s *Service) readArtifacts(ctx context.Context, owner uuid.UUID, j storageJob, input io.Reader) error {
	r, err := workspacesnapshot.ArtifactReader(io.LimitReader(input, s.cfg.Work.ArchiveLimit()+workspacesnapshot.ArtifactsManifestLimit+int64(s.cfg.Work.FileCountLimit())*1024+257))
	if err != nil {
		return err
	}
	part, err := r.NextPart()
	if err != nil {
		return fmt.Errorf("成果清单读取失败：%w", err)
	}
	if part.Header.Get("Content-Id") != "manifest" {
		return errors.New("成果清单缺失")
	}
	data, err := io.ReadAll(io.LimitReader(part, workspacesnapshot.ArtifactsManifestLimit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > workspacesnapshot.ArtifactsManifestLimit {
		return errors.New("成果清单过大")
	}
	var manifest workspacesnapshot.ArtifactsManifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	if manifest.Schema != workspacesnapshot.ArtifactsSchema || manifest.ID != j.ID.String() || len(manifest.Files) > s.cfg.Work.FileCountLimit() {
		return errors.New("成果清单校验失败")
	}
	seen := map[string]bool{}
	var total int64
	for _, file := range manifest.Files {
		if !workspacesnapshot.ArtifactPath(file.Path) || file.Type != "file" || file.Size < 0 || file.Size > s.cfg.Work.FileLimit() || len(file.SHA256) != 64 || seen[file.Path] {
			return errors.New("成果文件路径、大小或清单校验失败")
		}
		seen[file.Path] = true
		total += file.Size
		if total > s.cfg.Work.ArchiveLimit() {
			return errors.New("成果总大小超过保存限制")
		}
	}
	wanted := map[string]bool{}
	var request struct {
		Paths []string `json:"paths"`
	}
	if len(j.Request) > 0 {
		if err = json.Unmarshal(j.Request, &request); err != nil {
			return err
		}
	}
	for _, p := range request.Paths {
		name, valid := logicalOutputPath(p)
		if !valid {
			return errors.New("成果路径不在平台输出目录中")
		}
		wanted[name] = true
	}
	for _, item := range manifest.Files {
		if err = ctx.Err(); err != nil {
			return err
		}
		part, err = r.NextPart()
		if err != nil {
			return errors.New("成果内容缺失")
		}
		if part.Header.Get("Content-Id") != item.Path {
			return errors.New("成果文件与清单不一致")
		}
		file, digest, size, err := spool(part, s.cfg.Work.FileLimit())
		if err != nil {
			return err
		}
		if digest != item.SHA256 || size != item.Size {
			removeSpool(file)
			return errors.New("成果文件完整性校验失败")
		}
		if len(request.Paths) == 0 || wanted[item.Path] {
			err = s.saveArtifact(ctx, owner, j, item.Path, file, digest, size)
			delete(wanted, item.Path)
		}
		removeSpool(file)
		if err != nil {
			return err
		}
	}
	if _, err = r.NextPart(); !errors.Is(err, io.EOF) {
		return errors.New("成果传输未完整结束或包含额外文件")
	}
	if len(wanted) > 0 {
		return errors.New("所选成果文件不存在")
	}
	return nil
}

// Background preservation must still finish after a user's execution grants
// are revoked. It stores only private objects for the original task owner;
// every user-facing read, publish, and restore keeps its current access guard.
// An uncommitted job must match the current task environment. A committed job
// may finish conversion after a restore switches that mapping.
func (s *Service) archiveOwner(ctx context.Context, j storageJob) (uuid.UUID, error) {
	var owner uuid.UUID
	err := s.db.QueryRowContext(ctx, `SELECT w.owner_id FROM work_tasks w JOIN tasks t ON t.id=w.task_id JOIN runtime_environments e ON e.id=$2 WHERE w.task_id=$1 AND t.deleted_at IS NULL AND t.user_id=w.owner_id AND e.owner_id=w.owner_id AND (EXISTS(SELECT 1 FROM runtime_task_intents i WHERE i.task_id=w.task_id AND i.environment_id=e.id) OR EXISTS(SELECT 1 FROM workspace_checkpoints cp WHERE cp.id=$3 AND cp.task_id=w.task_id AND cp.environment_id=e.id AND cp.state IN ('committed','pruning','pruned')) OR EXISTS(SELECT 1 FROM work_storage_jobs j WHERE j.id=$3 AND j.task_id=w.task_id AND j.environment_id=e.id AND j.kind='artifacts' AND j.request->>'artifacts_saved'='true'))`, j.TaskID, j.EnvironmentID, j.ID).Scan(&owner)
	return owner, err
}

func (s *Service) saveArtifact(ctx context.Context, uid uuid.UUID, j storageJob, name string, f *os.File, digest string, size int64) error {
	aid := uuid.NewSHA1(j.TaskID, []byte(name))
	vid := uuid.New()
	base := path.Base(name)
	key := fmt.Sprintf("work-private/artifacts/%s/%s/%s", aid, vid, base)
	var old string
	err := s.db.QueryRowContext(ctx, `SELECT v.sha256 FROM work_artifacts a JOIN work_artifact_versions v ON v.id=a.current_version_id WHERE a.id=$1`, aid).Scan(&old)
	if err == nil && old == digest {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err = s.putVerified(ctx, key, f, digest, size); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO work_artifacts(id,owner_id,task_id,project_id,title,filename,source_path) SELECT $1,$2,$3,project_id,$4,$4,$5 FROM project_tasks WHERE task_id=$3 ON CONFLICT(id) DO NOTHING`, aid, uid, j.TaskID, base, name); err != nil {
		return err
	}
	var current sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT current_version_id::text FROM work_artifacts WHERE id=$1 FOR UPDATE`, aid).Scan(&current); err != nil {
		return err
	}
	mt := mime.TypeByExtension(strings.ToLower(path.Ext(base)))
	if mt == "" {
		mt = "application/octet-stream"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO work_artifact_versions(id,artifact_id,version,command_id,filename,mime_type,storage_key,sha256,size) SELECT $1,$2,COALESCE(max(version),0)+1,$3,$4,$5,$6,$7,$8 FROM work_artifact_versions WHERE artifact_id=$2 ON CONFLICT(artifact_id,command_id) DO NOTHING`, vid, aid, j.CommandID, base, mt, key, digest, size); err != nil {
		return err
	}
	if j.CommandID != nil {
		if err = tx.QueryRowContext(ctx, `SELECT id FROM work_artifact_versions WHERE artifact_id=$1 AND command_id=$2`, aid, j.CommandID).Scan(&vid); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE work_artifacts SET current_version_id=$2,deleted_at=NULL WHERE id=$1`, aid, vid); err != nil {
		return err
	}
	return tx.Commit()
}
