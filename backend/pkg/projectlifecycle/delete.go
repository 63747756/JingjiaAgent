// Package projectlifecycle removes a shared project without deleting private tasks.
package projectlifecycle

import (
	"context"
	"database/sql"
	"errors"

	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
)

func Delete(ctx context.Context, database *sql.DB, uid, pid uuid.UUID) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner uuid.UUID
	var deleted sql.NullTime
	var member bool
	err = tx.QueryRowContext(ctx, `SELECT user_id,deleted_at,team_id IS NULL OR EXISTS(SELECT 1 FROM team_members m JOIN teams t ON t.id=m.team_id WHERE m.team_id=p.team_id AND m.user_id=$2 AND t.deleted_at IS NULL) FROM projects p WHERE id=$1 FOR UPDATE`, pid, uid).Scan(&owner, &deleted, &member)
	if errors.Is(err, sql.ErrNoRows) {
		return errcode.ErrNotFound
	}
	if err != nil {
		return err
	}
	if owner != uid || !member {
		return errcode.ErrForbidden
	}
	if deleted.Valid {
		return nil
	}
	var busy bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM project_tasks pt JOIN tasks t ON t.id=pt.task_id WHERE pt.project_id=$1 AND t.deleted_at IS NULL AND (
 (t.status IN ('pending','processing') AND NOT EXISTS(SELECT 1 FROM runtime_task_intents i WHERE i.task_id=t.id)) OR EXISTS(SELECT 1 FROM runtime_commands c WHERE c.task_id=t.id AND c.state NOT IN ('complete','failed','canceled'))
 OR EXISTS(SELECT 1 FROM runtime_restores r WHERE r.task_id=t.id AND r.state NOT IN ('complete','failed'))
 OR EXISTS(SELECT 1 FROM work_storage_jobs j WHERE j.task_id=t.id AND j.state IN ('pending','running'))))`, pid).Scan(&busy)
	if err != nil {
		return err
	}
	if busy {
		return errcode.ErrProjectBusy
	}
	// Only current members retain their private tasks; project deletion must not
	// restore access for a previously removed member.
	_, err = tx.ExecContext(ctx, `CREATE TEMP TABLE retained_project_tasks ON COMMIT DROP AS SELECT t.id FROM tasks t JOIN project_tasks pt ON pt.task_id=t.id JOIN projects p ON p.id=pt.project_id
 WHERE p.id=$1 AND t.deleted_at IS NULL AND (p.user_id=t.user_id OR EXISTS(SELECT 1 FROM project_collaborators c WHERE c.project_id=p.id AND c.user_id=t.user_id AND c.deleted_at IS NULL))
 AND (p.team_id IS NULL OR EXISTS(SELECT 1 FROM team_members m JOIN teams te ON te.id=m.team_id WHERE m.team_id=p.team_id AND m.user_id=t.user_id AND te.deleted_at IS NULL))`, pid)
	if err != nil {
		return err
	}
	// Preserve only the exact versions already referenced by each retained task.
	// Bytes and historical manifests stay immutable; no shared folder is copied.
	statements := []string{
		`UPDATE work_tasks w SET retained_resource_versions=w.retained_resource_versions || COALESCE((SELECT jsonb_agg(pin->>'version_id') FROM jsonb_array_elements(w.resource_manifest) pin JOIN work_resources r ON r.id::text=pin->>'resource_id' AND r.project_id=$1 AND r.deleted_at IS NULL JOIN work_resource_versions v ON v.resource_id=r.id AND v.id::text=pin->>'version_id'), '[]'::jsonb) WHERE w.task_id IN (SELECT id FROM retained_project_tasks)`,
		`UPDATE project_tasks SET project_id=NULL WHERE project_id=$1 AND task_id IN (SELECT id FROM retained_project_tasks)`,
		`UPDATE work_artifacts SET project_id=NULL WHERE project_id=$1 AND task_id IN (SELECT id FROM retained_project_tasks)`,
		// Only an uploader who still has project and team access may keep the
		// whole resource as a personal file. Otherwise retain the deleted project
		// as its access boundary; retained tasks already have exact-version grants.
		`UPDATE work_resources r SET project_id=NULL FROM projects p WHERE r.project_id=$1 AND p.id=r.project_id
 AND (p.user_id=r.owner_id OR EXISTS(SELECT 1 FROM project_collaborators c WHERE c.project_id=p.id AND c.user_id=r.owner_id AND c.deleted_at IS NULL))
 AND (p.team_id IS NULL OR EXISTS(SELECT 1 FROM team_members m JOIN teams te ON te.id=m.team_id WHERE m.team_id=p.team_id AND m.user_id=r.owner_id AND te.deleted_at IS NULL))`,
		`DELETE FROM work_artifact_publications WHERE project_id=$1`,
		`UPDATE project_collaborators SET deleted_at=now() WHERE project_id=$1 AND deleted_at IS NULL`,
		`UPDATE project_issue_comments SET deleted_at=now() WHERE issue_id IN (SELECT id FROM project_issues WHERE project_id=$1) AND deleted_at IS NULL`,
		`UPDATE project_issues SET deleted_at=now() WHERE project_id=$1 AND deleted_at IS NULL`,
		`DELETE FROM project_git_bots WHERE project_id=$1`,
		`DELETE FROM navigation_pins WHERE kind='project' AND target_id=$1`,
		`UPDATE projects SET deleted_at=now(),updated_at=now() WHERE id=$1`,
	}
	for _, q := range statements {
		if _, err = tx.ExecContext(ctx, q, pid); err != nil {
			return err
		}
	}
	return tx.Commit()
}
