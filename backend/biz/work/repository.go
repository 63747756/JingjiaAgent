package work

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
	"github.com/samber/do"
	"sync"
	"time"
)

type Repository struct {
	db     *sql.DB
	lockDB *sql.DB
}

const requestLockTimeout = 30 * time.Second

func NewRepository(i *do.Injector) (*Repository, error) {
	cfg := do.MustInvoke[*config.Config](i)
	return openRepository(cfg.Database.Master)
}

func openRepository(dsn string) (*Repository, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
	// Session advisory locks live for the entire request. They must not consume
	// the pool needed by that same request's business queries.
	lockDB, err := sql.Open("postgres", dsn)
	if err != nil {
		db.Close()
		return nil, err
	}
	lockDB.SetMaxOpenConns(8)
	lockDB.SetMaxIdleConns(2)
	return &Repository{db: db, lockDB: lockDB}, nil
}
func (r *Repository) Shutdown() error {
	if r.lockDB == nil {
		return r.db.Close()
	}
	return errors.Join(r.lockDB.Close(), r.db.Close())
}

const profileColumns = `id,team_id,name,description,model_id,host_id,image_id,cli_name,required_skill_ids,core,memory,enabled,is_default,instructions,task_presets,mentionable_skill_ids`

type rowScanner interface{ Scan(...any) error }

func scanProfile(row rowScanner) (Profile, error) {
	var p Profile
	var skills, presets, mentionable []byte
	err := row.Scan(&p.ID, &p.TeamID, &p.Name, &p.Description, &p.ModelID, &p.HostID, &p.ImageID, &p.CliName, &skills, &p.Core, &p.Memory, &p.Enabled, &p.IsDefault, &p.Instructions, &presets, &mentionable)
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(skills, &p.RequiredSkillIDs); err != nil {
		return p, err
	}
	if err = json.Unmarshal(mentionable, &p.MentionableSkillIDs); err != nil {
		return p, err
	}
	err = json.Unmarshal(presets, &p.TaskPresets)
	return p, err
}
func (r *Repository) profiles(ctx context.Context, uid uuid.UUID, all bool) ([]Profile, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+profileColumns+` FROM work_execution_profiles p WHERE EXISTS(SELECT 1 FROM team_members m JOIN teams t ON t.id=m.team_id WHERE m.user_id=$1 AND m.team_id=p.team_id AND t.deleted_at IS NULL AND (NOT $2 OR m.role='admin')) AND ($2 OR p.enabled) ORDER BY p.is_default DESC,p.name,p.id`, uid, all)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Profile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (r *Repository) metadata(ctx context.Context, tid uuid.UUID) (Metadata, error) {
	var m Metadata
	var skills, resources []byte
	var preset []byte
	err := r.db.QueryRowContext(ctx, `SELECT profile_id,team_id,skill_manifest,resource_manifest,output_requirements,delivery_status,created_at,assistant_name,task_preset FROM work_tasks WHERE task_id=$1`, tid).Scan(&m.ProfileID, &m.TeamID, &skills, &resources, &m.OutputRequirements, &m.BusinessStatus, &m.CreatedAt, &m.AssistantName, &preset)
	if errors.Is(err, sql.ErrNoRows) {
		return m, errcode.ErrNotFound
	}
	if err != nil {
		return m, err
	}
	if err = json.Unmarshal(skills, &m.SkillPins); err != nil {
		return m, err
	}
	if len(preset) > 0 && string(preset) != "null" {
		if err = json.Unmarshal(preset, &m.TaskPreset); err != nil {
			return m, err
		}
	}
	err = json.Unmarshal(resources, &m.InputResources)
	return m, err
}

type AccessGuard struct{ store *Repository }

func (g *AccessGuard) CheckWorkTaskAccess(ctx context.Context, uid, tid uuid.UUID, write bool) error {
	var owner, team uuid.UUID
	var pid sql.NullString
	var deleted bool
	err := g.store.db.QueryRowContext(ctx, `SELECT w.owner_id,w.team_id,pt.project_id::text,(t.id IS NULL OR t.deleted_at IS NOT NULL OR pt.task_id IS NULL) FROM work_tasks w LEFT JOIN project_tasks pt ON pt.task_id=w.task_id LEFT JOIN tasks t ON t.id=w.task_id WHERE w.task_id=$1`, tid).Scan(&owner, &team, &pid, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if owner != uid || deleted {
		return errcode.ErrNotFound
	}
	var allowed bool
	if err = g.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM team_members m JOIN teams t ON t.id=m.team_id WHERE m.user_id=$1 AND m.team_id=$2 AND t.deleted_at IS NULL)`, uid, team).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return errcode.ErrForbidden
	}
	if pid.Valid {
		err = g.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projects p WHERE p.id=$1 AND p.deleted_at IS NULL AND (p.user_id=$2 OR EXISTS(SELECT 1 FROM project_collaborators c WHERE c.project_id=p.id AND c.user_id=$2 AND c.deleted_at IS NULL AND (NOT $3 OR c.role='read_write'))))`, pid.String, uid, write).Scan(&allowed)
		if err != nil {
			return err
		}
		if !allowed {
			return errcode.ErrForbidden
		}
	}
	// Skill enablement is a next-turn preference, not access to task history.
	// The round resolver rechecks live skill grants before accepting each turn.
	err = g.store.db.QueryRowContext(ctx, `SELECT NOT EXISTS(
 SELECT 1 FROM work_tasks w CROSS JOIN LATERAL jsonb_array_elements(w.resource_manifest) pin
 WHERE w.task_id=$1 AND NOT EXISTS(
  SELECT 1 FROM work_resources r JOIN work_resource_versions v ON v.resource_id=r.id
  WHERE r.id::text=pin->>'resource_id' AND v.id::text=pin->>'version_id' AND r.deleted_at IS NULL
   AND (w.retained_resource_versions @> jsonb_build_array(pin->>'version_id') OR (r.project_id IS NULL AND r.owner_id=$2) OR EXISTS(
    SELECT 1 FROM projects p WHERE p.id=r.project_id AND p.deleted_at IS NULL
     AND (p.team_id IS NULL OR EXISTS(SELECT 1 FROM team_members tm JOIN teams te ON te.id=tm.team_id
       WHERE tm.team_id=p.team_id AND tm.user_id=$2 AND te.deleted_at IS NULL))
     AND (p.user_id=$2 OR EXISTS(SELECT 1 FROM project_collaborators pc
       WHERE pc.project_id=p.id AND pc.user_id=$2 AND pc.deleted_at IS NULL))))) )`, tid, uid).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return errcode.ErrForbidden
	}
	return nil
}

func (r *Repository) requestLock(ctx context.Context, uid, rid uuid.UUID) (func(), error) {
	if r.lockDB == nil {
		return nil, errors.New("request lock pool is not configured")
	}
	lockCtx, cancel := context.WithTimeout(ctx, requestLockTimeout)
	defer cancel()
	conn, err := r.lockDB.Conn(lockCtx)
	if err != nil {
		return nil, err
	}
	// PostgreSQL coordinates retries across processes, including a lost HTTP
	// response after a committed admission. Only this owner's request is locked.
	key := uid.String() + ":" + rid.String()
	if _, err = conn.ExecContext(lockCtx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, key); err != nil {
		// A canceled query can race with acquisition. Never return a possibly
		// locked session to the pool.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		conn.Close()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), requestUnlockTimeout)
			defer cancel()
			if _, err := conn.ExecContext(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key); err != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
			conn.Close()
		})
	}, nil
}
func (r *Repository) priorRequest(ctx context.Context, uid, rid uuid.UUID, hash string) (uuid.UUID, error) {
	var id uuid.UUID
	var original string
	err := r.db.QueryRowContext(ctx, `SELECT task_id,request_payload_hash FROM work_tasks WHERE owner_id=$1 AND client_request_id=$2`, uid, rid).Scan(&id, &original)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, err
	}
	if original != hash {
		return uuid.Nil, errcode.ErrBadRequest.Wrap(fmt.Errorf("同一请求标识不能用于不同工作任务"))
	}
	return id, nil
}
