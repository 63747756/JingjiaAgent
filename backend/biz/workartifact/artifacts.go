package workartifact

import (
	"context"
	"database/sql"
	"errors"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
	"io"
)

func (s *Service) artifactAccess(ctx context.Context, uid, aid, vid uuid.UUID) error {
	var owner uuid.UUID
	var task uuid.UUID
	var project sql.NullString
	var team sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT a.owner_id,a.task_id,pt.project_id::text,w.team_id::text FROM work_artifacts a LEFT JOIN work_tasks w ON w.task_id=a.task_id LEFT JOIN project_tasks pt ON pt.task_id=a.task_id WHERE a.id=$1 AND a.deleted_at IS NULL`, aid).Scan(&owner, &task, &project, &team)
	if errors.Is(err, sql.ErrNoRows) {
		return errcode.ErrNotFound
	}
	if err != nil {
		return err
	}
	if owner == uid {
		if s.guard != nil {
			if err = s.guard.CheckWorkTaskAccess(ctx, uid, task, false); err != nil {
				return err
			}
		}
		if team.Valid {
			var member bool
			if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM team_members m JOIN teams t ON t.id=m.team_id WHERE m.team_id=$1 AND m.user_id=$2 AND t.deleted_at IS NULL)`, team.String, uid).Scan(&member); err != nil {
				return err
			}
			if !member {
				return errcode.ErrForbidden
			}
		}
		if project.Valid {
			pid, _ := uuid.Parse(project.String)
			return s.projectPermission(ctx, uid, pid, false)
		}
		return nil
	}
	var allowed bool
	err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_artifact_publications x JOIN projects p ON p.id=x.project_id AND p.deleted_at IS NULL WHERE x.artifact_id=$1 AND x.version_id=$3 AND (p.team_id IS NULL OR EXISTS(SELECT 1 FROM team_members tm JOIN teams te ON te.id=tm.team_id WHERE tm.team_id=p.team_id AND tm.user_id=$2 AND te.deleted_at IS NULL)) AND (p.user_id=$2 OR EXISTS(SELECT 1 FROM project_collaborators c WHERE c.project_id=p.id AND c.user_id=$2 AND c.deleted_at IS NULL)))`, aid, uid, vid).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return errcode.ErrForbidden
	}
	return nil
}

func (s *Service) Versions(ctx context.Context, uid, aid uuid.UUID) ([]ArtifactVersion, error) {
	if err := s.enabled(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,artifact_id,version,filename,mime_type,size,sha256,validation_status,preview_status,created_at,storage_key,preview_key FROM work_artifact_versions WHERE artifact_id=$1 ORDER BY version DESC`, aid)
	if err != nil {
		return nil, err
	}
	all := []ArtifactVersion{}
	for rows.Next() {
		var v ArtifactVersion
		if err = rows.Scan(&v.ID, &v.ArtifactID, &v.Version, &v.Filename, &v.MIMEType, &v.Size, &v.SHA256, &v.ValidationStatus, &v.PreviewStatus, &v.CreatedAt, &v.StorageKey, &v.PreviewKey); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []ArtifactVersion{}
	for _, v := range all {
		err = s.artifactAccess(ctx, uid, aid, v.ID)
		if errors.Is(err, errcode.ErrForbidden) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err = s.prepareArtifactPreview(ctx, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, errcode.ErrNotFound
	}
	return out, nil
}

// Upgrade old native previews only after checking access. List pages prepare
// their current version; historical versions are prepared when requested.
func (s *Service) prepareArtifactPreview(ctx context.Context, v *ArtifactVersion) error {
	if v.PreviewStatus != "unavailable" || nativePreviewMIME(v.Filename) == "" {
		return nil
	}
	if err := s.convertVersion(ctx, *v); err != nil {
		return err
	}
	return s.db.QueryRowContext(ctx, `SELECT validation_status,preview_status,preview_key FROM work_artifact_versions WHERE id=$1`, v.ID).Scan(&v.ValidationStatus, &v.PreviewStatus, &v.PreviewKey)
}

func (s *Service) Artifact(ctx context.Context, uid, aid uuid.UUID) (Artifact, error) {
	var out Artifact
	var pid sql.NullString
	var owner uuid.UUID
	var current uuid.UUID
	err := s.db.QueryRowContext(ctx, `SELECT id,task_id,project_id::text,title,filename,owner_id,current_version_id FROM work_artifacts WHERE id=$1 AND deleted_at IS NULL`, aid).Scan(&out.ID, &out.TaskID, &pid, &out.Title, &out.Filename, &owner, &current)
	if err != nil {
		return out, err
	}
	if pid.Valid {
		p, _ := uuid.Parse(pid.String)
		out.ProjectID = &p
	}
	out.Versions, err = s.Versions(ctx, uid, aid)
	if err != nil {
		return out, err
	}
	// The newest authorized version, not the owner's private current version,
	// is projected for a collaborator who only received a published version.
	v := out.Versions[0]
	if owner == uid {
		// Historical spreadsheet conversion may append a derived version after
		// a newer round has completed. Its version number does not make it current.
		for _, version := range out.Versions {
			if version.ID == current {
				v = version
				break
			}
		}
	}
	out.VersionCount = len(out.Versions)
	out.CurrentVersion = &v
	out.CurrentVersionID = v.ID
	out.Filename = v.Filename
	out.ValidationStatus = v.ValidationStatus
	out.PreviewStatus = v.PreviewStatus
	out.CanEdit = owner == uid
	if !out.CanEdit {
		out.TaskID = uuid.Nil
	}
	return out, nil
}

func (s *Service) Publish(ctx context.Context, uid, aid, pid, vid uuid.UUID) error {
	if err := s.projectPermission(ctx, uid, pid, true); err != nil {
		return err
	}
	var owner uuid.UUID
	var task uuid.UUID
	var project sql.NullString
	var current uuid.UUID
	if err := s.db.QueryRowContext(ctx, `SELECT a.owner_id,a.task_id,pt.project_id::text,a.current_version_id FROM work_artifacts a JOIN work_tasks w ON w.task_id=a.task_id JOIN project_tasks pt ON pt.task_id=a.task_id WHERE a.id=$1 AND a.deleted_at IS NULL`, aid).Scan(&owner, &task, &project, &current); err != nil {
		return err
	}
	if owner != uid {
		return errcode.ErrForbidden
	}
	if s.guard != nil {
		if err := s.guard.CheckWorkTaskAccess(ctx, uid, task, true); err != nil {
			return err
		}
	}
	if project.Valid {
		p, _ := uuid.Parse(project.String)
		if p != pid {
			return errcode.ErrForbidden
		}
	}
	if vid == uuid.Nil {
		vid = current
	}
	if err := s.artifactAccess(ctx, uid, aid, vid); err != nil {
		return err
	}
	var valid bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_artifact_versions WHERE id=$1 AND artifact_id=$2 AND validation_status='passed')`, vid, aid).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return errcode.ErrBadRequest.Wrap(errors.New("只有校验通过的成果版本可以发布"))
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO work_artifact_publications(artifact_id,version_id,project_id,published_by) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, aid, vid, pid, uid)
	return err
}

func (s *Service) Download(ctx context.Context, uid, aid, vid uuid.UUID, preview bool) (io.ReadCloser, string, string, error) {
	versions, err := s.Versions(ctx, uid, aid)
	if err != nil {
		return nil, "", "", err
	}
	if vid == uuid.Nil {
		err = s.db.QueryRowContext(ctx, `SELECT current_version_id FROM work_artifacts WHERE id=$1 AND owner_id=$2 AND deleted_at IS NULL`, aid, uid).Scan(&vid)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, "", "", err
		}
	}
	for _, v := range versions {
		if vid != uuid.Nil && v.ID != vid {
			continue
		}
		key := v.StorageKey
		name := v.Filename
		mime := v.MIMEType
		if preview {
			if v.PreviewStatus != "ready" || v.PreviewKey == "" {
				return nil, "", "", errcode.ErrNotFound
			}
			key = v.PreviewKey
			if native := nativePreviewMIME(v.Filename); native != "" {
				mime = native
			} else {
				name, mime = "preview.pdf", "application/pdf"
			}
		}
		r, err := s.store.GetObject(ctx, key)
		return r, name, mime, err
	}
	return nil, "", "", errcode.ErrNotFound
}

func (s *Service) DeleteArtifact(ctx context.Context, uid, aid uuid.UUID) error {
	var owner uuid.UUID
	var task uuid.UUID
	if err := s.db.QueryRowContext(ctx, `SELECT owner_id,task_id FROM work_artifacts WHERE id=$1 AND deleted_at IS NULL`, aid).Scan(&owner, &task); err != nil {
		return err
	}
	if owner != uid {
		return errcode.ErrForbidden
	}
	if s.guard != nil {
		if err := s.guard.CheckWorkTaskAccess(ctx, uid, task, true); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE work_artifacts SET deleted_at=now() WHERE id=$1`, aid)
	return err
}
