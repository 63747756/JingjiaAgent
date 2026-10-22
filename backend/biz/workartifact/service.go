package workartifact

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"

	"github.com/63747756/jingjiaagent/backend/biz/agentresource"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/asseturl"
	"github.com/63747756/jingjiaagent/backend/pkg/delayqueue"
	"github.com/63747756/jingjiaagent/backend/pkg/oss"
	"github.com/63747756/jingjiaagent/backend/pkg/runtimeadapter"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
	"github.com/samber/do"
)

type checkpointRuntime interface {
	ExportCheckpoint(context.Context, uuid.UUID, uuid.UUID, string) (io.ReadCloser, error)
	ExportArtifacts(context.Context, uuid.UUID, uuid.UUID, string) (io.ReadCloser, error)
	CompleteCheckpoint(context.Context, uuid.UUID, uuid.UUID, string) error
	RestoreCheckpoint(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, string, string, []domain.TaskAttachment) (string, error)
	SetContinuationRestore(func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (string, error))
	Continuations(context.Context, uuid.UUID, uuid.UUID) ([]runtimeadapter.ContinuationStatus, error)
	RetryContinuation(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, *uuid.UUID, bool) error
}

type Service struct {
	db           *sql.DB
	store        agentresource.ObjectStore
	cfg          *config.Config
	runtime      checkpointRuntime
	logger       *slog.Logger
	guard        domain.WorkAccessGuard
	recycleQueue *delayqueue.VMRecycleQueue
}

func NewService(i *do.Injector) (*Service, error) {
	cfg := do.MustInvoke[*config.Config](i)
	db, err := sql.Open("postgres", cfg.Database.Master)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(6)
	db.SetMaxIdleConns(2)
	s := &Service{db: db, cfg: cfg, logger: do.MustInvoke[*slog.Logger](i)}
	s.recycleQueue, _ = do.Invoke[*delayqueue.VMRecycleQueue](i)
	s.guard, _ = do.Invoke[domain.WorkAccessGuard](i)
	if cfg.Work.Enabled {
		if !cfg.ObjectStorage.Enabled {
			db.Close()
			return nil, errors.New("work requires private object storage")
		}
		store, err := oss.NewS3Compatible(context.Background(), cfg.ObjectStorage, oss.S3Option{ForcePathStyle: cfg.ObjectStorage.ForcePathStyle, InitBucket: cfg.ObjectStorage.InitBucket})
		if err != nil {
			db.Close()
			return nil, err
		}
		if strings.TrimSpace(cfg.ObjectStorage.AgentAccessEndpoint) != "" {
			store = store.WithAccessEndpoint(cfg.ObjectStorage.AgentAccessEndpoint)
		}
		s.store = store
	}
	if runtime, ok := do.MustInvoke[taskflow.Clienter](i).(*runtimeadapter.Client); ok && runtime != nil {
		s.runtime = runtime
	}
	if s.runtime != nil && cfg.Work.Enabled {
		s.runtime.SetContinuationRestore(s.restoreContinuation)
	}
	return s, nil
}

func (s *Service) enabled() error {
	if s.cfg == nil || !s.cfg.Work.Enabled || s.store == nil {
		return errcode.ErrBadRequest.Wrap(errors.New("工作平台尚未启用或存储未配置"))
	}
	return nil
}

func (s *Service) projectPermission(ctx context.Context, uid, pid uuid.UUID, write bool) error {
	if pid == uuid.Nil {
		return errcode.ErrBadRequest
	}
	var allowed bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projects p WHERE p.id=$1 AND p.deleted_at IS NULL AND (p.team_id IS NULL OR EXISTS(SELECT 1 FROM team_members tm JOIN teams te ON te.id=tm.team_id WHERE tm.team_id=p.team_id AND tm.user_id=$2 AND te.deleted_at IS NULL)) AND (p.user_id=$2 OR EXISTS(SELECT 1 FROM project_collaborators c WHERE c.project_id=p.id AND c.user_id=$2 AND c.deleted_at IS NULL AND (NOT $3 OR c.role='read_write'))))`, pid, uid, write).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return errcode.ErrForbidden
	}
	return nil
}

func (s *Service) taskPermission(ctx context.Context, uid, tid uuid.UUID) (*uuid.UUID, error) {
	if s.guard != nil {
		if err := s.guard.CheckWorkTaskAccess(ctx, uid, tid, false); err != nil {
			return nil, err
		}
	}
	var owner uuid.UUID
	var pid sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT t.user_id,pt.project_id::text FROM tasks t LEFT JOIN project_tasks pt ON pt.task_id=t.id WHERE t.id=$1 AND t.deleted_at IS NULL`, tid).Scan(&owner, &pid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errcode.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if owner != uid {
		return nil, errcode.ErrForbidden
	}
	if pid.Valid {
		id, err := uuid.Parse(pid.String)
		if err != nil {
			return nil, err
		}
		if err = s.projectPermission(ctx, uid, id, false); err != nil {
			return nil, err
		}
		return &id, nil
	}
	return nil, nil
}

func filename(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00\r\n") || len(name) > 255 {
		return "", errcode.ErrBadRequest.Wrap(errors.New("文件名无效"))
	}
	return name, nil
}

// spool bounds memory and verifies the exact bytes that will be stored.
func spool(r io.Reader, max int64) (*os.File, string, int64, error) {
	f, err := os.CreateTemp("", "jingjiaagent-work-*")
	if err != nil {
		return nil, "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, max+1))
	if err == nil && n > max {
		err = errors.New("文件超过工作平台大小限制")
	}
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, "", 0, err
	}
	_, err = f.Seek(0, io.SeekStart)
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, "", 0, err
	}
	return f, hex.EncodeToString(h.Sum(nil)), n, nil
}
func removeSpool(f *os.File) {
	if f != nil {
		f.Close()
		os.Remove(f.Name())
	}
}

func (s *Service) putVerified(ctx context.Context, key string, f *os.File, digest string, size int64) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := s.store.PutFile(ctx, path.Dir(key), path.Base(key), f); err != nil {
		return err
	}
	r, err := s.store.GetObject(ctx, key)
	if err != nil {
		return err
	}
	defer r.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, size+1))
	if err != nil {
		return err
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != digest {
		return errors.New("对象存储内容校验失败")
	}
	return nil
}

func (s *Service) RegisterResource(ctx context.Context, uid uuid.UUID, req RegisterResourceRequest) (Resource, error) {
	return s.registerResource(ctx, uid, uuid.Nil, req)
}
func (s *Service) AddResourceVersion(ctx context.Context, uid, rid uuid.UUID, req RegisterResourceRequest) (Resource, error) {
	return s.registerResource(ctx, uid, rid, req)
}
func (s *Service) registerResource(ctx context.Context, uid, rid uuid.UUID, req RegisterResourceRequest) (Resource, error) {
	var out Resource
	if err := s.enabled(); err != nil {
		return out, err
	}
	name, err := filename(req.Filename)
	if err != nil {
		return out, err
	}
	key, ok := asseturl.Parse(req.URL)
	// Only a caller-owned upload can become a resource. Public assets and
	// arbitrary URLs must never be copied through the privileged storage client.
	temp := strings.Trim(s.cfg.ObjectStorage.TempPrefix, "/") + "/"
	if !ok || !strings.HasPrefix(key, temp) || !strings.HasPrefix(path.Base(key), uid.String()+"_") {
		return out, errcode.ErrForbidden
	}
	if rid != uuid.Nil {
		var owner uuid.UUID
		var pid sql.NullString
		if err = s.db.QueryRowContext(ctx, `SELECT owner_id,project_id::text FROM work_resources WHERE id=$1 AND deleted_at IS NULL`, rid).Scan(&owner, &pid); err != nil {
			return out, err
		}
		if pid.Valid {
			id, _ := uuid.Parse(pid.String)
			req.ProjectID = &id
		} else if owner != uid {
			return out, errcode.ErrForbidden
		}
	}
	if req.ProjectID != nil {
		if err = s.projectPermission(ctx, uid, *req.ProjectID, true); err != nil {
			return out, err
		}
	}
	r, err := s.store.GetObject(ctx, key)
	if err != nil {
		return out, err
	}
	defer r.Close()
	f, digest, size, err := spool(r, s.cfg.Work.FileLimit())
	if err != nil {
		return out, err
	}
	defer removeSpool(f)
	if rid == uuid.Nil {
		rid = uuid.New()
	}
	vid := uuid.New()
	dest := fmt.Sprintf("work-private/resources/%s/%s/%s", rid, vid, name)
	if err = s.putVerified(ctx, dest, f, digest, size); err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO work_resources(id,owner_id,project_id,filename) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING`, rid, uid, req.ProjectID, name); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO work_resource_versions(id,resource_id,filename,storage_key,sha256,size) VALUES($1,$2,$3,$4,$5,$6)`, vid, rid, name, dest, digest, size); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE work_resources SET current_version_id=$2,filename=$3 WHERE id=$1`, rid, vid, name); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return s.Resource(ctx, uid, rid)
}

func (s *Service) Resource(ctx context.Context, uid, rid uuid.UUID) (Resource, error) {
	var out Resource
	var pid sql.NullString
	restricted := false
	err := s.db.QueryRowContext(ctx, `SELECT id,owner_id,project_id::text,filename,current_version_id FROM work_resources WHERE id=$1 AND deleted_at IS NULL`, rid).Scan(&out.ID, &out.OwnerID, &pid, &out.Filename, &out.CurrentVersionID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, errcode.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if pid.Valid {
		id, _ := uuid.Parse(pid.String)
		out.ProjectID = &id
		if err = s.projectPermission(ctx, uid, id, false); err != nil {
			if !errors.Is(err, errcode.ErrForbidden) {
				return out, err
			}
			restricted = true
		}
	} else if out.OwnerID != uid {
		restricted = true
	}
	rows, err := s.db.QueryContext(ctx, `SELECT v.id,v.resource_id,v.filename,v.sha256,v.size,v.created_at,v.storage_key FROM work_resource_versions v WHERE v.resource_id=$1 AND (NOT $3 OR EXISTS(
 SELECT 1 FROM work_tasks w JOIN tasks t ON t.id=w.task_id AND t.deleted_at IS NULL JOIN team_members m ON m.team_id=w.team_id AND m.user_id=$2 JOIN teams te ON te.id=m.team_id AND te.deleted_at IS NULL
 WHERE w.owner_id=$2 AND w.retained_resource_versions @> jsonb_build_array(v.id::text))) ORDER BY v.created_at DESC,v.id DESC`, rid, uid, restricted)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	out.Versions = []ResourceVersion{}
	for rows.Next() {
		var v ResourceVersion
		if err = rows.Scan(&v.ID, &v.ResourceID, &v.Filename, &v.SHA256, &v.Size, &v.CreatedAt, &v.StorageKey); err != nil {
			return out, err
		}
		out.Versions = append(out.Versions, v)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if restricted {
		if len(out.Versions) == 0 {
			return Resource{}, errcode.ErrForbidden
		}
		out.ProjectID = nil
		out.CurrentVersionID = out.Versions[0].ID
		out.Filename = out.Versions[0].Filename
	}
	return out, nil
}

func (s *Service) ListResources(ctx context.Context, uid uuid.UUID, pid *uuid.UUID) ([]Resource, error) {
	if err := s.enabled(); err != nil {
		return nil, err
	}
	if pid != nil {
		if err := s.projectPermission(ctx, uid, *pid, false); err != nil {
			return nil, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM work_resources WHERE deleted_at IS NULL AND (($2::uuid IS NOT NULL AND project_id=$2) OR ($2::uuid IS NULL AND owner_id=$1 AND project_id IS NULL)) ORDER BY created_at DESC LIMIT 200`, uid, pid)
	if err != nil {
		return nil, err
	}
	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []Resource{}
	for _, id := range ids {
		r, err := s.Resource(ctx, uid, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *Service) ResolveInputs(ctx context.Context, uid uuid.UUID, pid *uuid.UUID, ids []uuid.UUID) ([]domain.TaskAttachment, []InputSnapshot, error) {
	if err := s.enabled(); err != nil {
		return nil, nil, err
	}
	if len(ids) > 10 {
		return nil, nil, errcode.ErrBadRequest.Wrap(errors.New("最多选择10份资料"))
	}
	out := []domain.TaskAttachment{}
	snapshots := []InputSnapshot{}
	seen := map[uuid.UUID]bool{}
	var totalSize int64
	for _, vid := range ids {
		if seen[vid] {
			continue
		}
		seen[vid] = true
		var rid uuid.UUID
		if err := s.db.QueryRowContext(ctx, `SELECT resource_id FROM work_resource_versions WHERE id=$1`, vid).Scan(&rid); err != nil {
			return nil, nil, err
		}
		res, err := s.Resource(ctx, uid, rid)
		if err != nil {
			return nil, nil, err
		}
		if res.ProjectID != nil && (pid == nil || *res.ProjectID != *pid) {
			return nil, nil, errcode.ErrForbidden
		}
		found := false
		for _, v := range res.Versions {
			if v.ID != vid {
				continue
			}
			found = true
			if v.Size > 32<<20 {
				return nil, nil, errcode.ErrBadRequest.Wrap(errors.New("工作输入单个文件不能超过32MiB"))
			}
			totalSize += v.Size
			if totalSize > 256<<20 {
				return nil, nil, errcode.ErrBadRequest.Wrap(errors.New("每轮附件总大小不能超过256MiB"))
			}
			r, err := s.store.GetObject(ctx, v.StorageKey)
			if err != nil {
				return nil, nil, err
			}
			f, digest, size, err := spool(r, s.cfg.Work.FileLimit())
			r.Close()
			if err != nil {
				return nil, nil, err
			}
			if digest != v.SHA256 || size != v.Size {
				removeSpool(f)
				return nil, nil, errors.New("资料版本内容已损坏")
			}
			copyKey := path.Join(s.cfg.ObjectStorage.TempPrefix, uid.String()+"_work_"+vid.String()+"_"+v.Filename)
			err = s.putVerified(ctx, copyKey, f, digest, size)
			removeSpool(f)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, domain.TaskAttachment{URL: asseturl.Build(copyKey), Filename: v.Filename})
			snapshots = append(snapshots, InputSnapshot{ResourceID: rid, VersionID: vid, Filename: v.Filename, SHA256: v.SHA256, Size: v.Size, ProjectID: res.ProjectID})
		}
		if !found {
			return nil, nil, errcode.ErrForbidden
		}
	}
	return out, snapshots, nil
}

func (s *Service) queue(ctx context.Context, uid, tid uuid.UUID, paths []string) (uuid.UUID, error) {
	if err := s.enabled(); err != nil {
		return uuid.Nil, err
	}
	if s.guard != nil {
		if err := s.guard.CheckWorkTaskAccess(ctx, uid, tid, true); err != nil {
			return uuid.Nil, err
		}
	}
	if _, err := s.taskPermission(ctx, uid, tid); err != nil {
		return uuid.Nil, err
	}
	paths = append([]string{}, paths...)
	for i, p := range paths {
		name, valid := logicalOutputPath(p)
		if !valid {
			return uuid.Nil, errcode.ErrBadRequest.Wrap(errors.New("只能登记.jingjiaagent/output目录中的普通文件"))
		}
		paths[i] = name
	}
	var env string
	if err := s.db.QueryRowContext(ctx, `SELECT environment_id FROM runtime_task_intents WHERE task_id=$1`, tid).Scan(&env); err != nil {
		return uuid.Nil, err
	}
	id := uuid.New()
	_, err := s.db.ExecContext(ctx, `INSERT INTO work_storage_jobs(id,task_id,environment_id,kind,request) VALUES($1,$2,$3,'artifacts',jsonb_build_object('paths',$4::jsonb,'scope','artifacts'))`, id, tid, env, jsonBytes(paths))
	return id, err
}

func validOutputPath(p string) bool {
	_, valid := logicalOutputPath(p)
	return valid
}

func logicalOutputPath(p string) (string, bool) {
	p = strings.TrimPrefix(p, "/workspace/")
	p = strings.TrimPrefix(p, ".jingjiaagent/")
	return p, strings.HasPrefix(p, "output/") && p == path.Clean(p) && !strings.ContainsAny(p, "\\\x00\r\n") && !strings.HasSuffix(p, "/")
}
