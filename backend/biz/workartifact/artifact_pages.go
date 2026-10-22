package workartifact

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
)

type artifactCursor struct {
	CreatedAt time.Time `json:"at"`
	ID        uuid.UUID `json:"id"`
}

func decodeArtifactCursor(value string) (*artifactCursor, error) {
	if value == "" {
		return nil, nil
	}
	if len(value) > 512 {
		return nil, errcode.ErrBadRequest
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	var cursor artifactCursor
	if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.ID == uuid.Nil || cursor.CreatedAt.IsZero() {
		return nil, errcode.ErrBadRequest
	}
	return &cursor, nil
}

func encodeArtifactCursor(cursor artifactCursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

// Keep the same publication permissions as Versions, including team revocation.
// The window count includes only versions this employee can actually read.
func (s *Service) artifactSummary(ctx context.Context, uid, aid uuid.UUID) (Artifact, error) {
	var out Artifact
	var owner uuid.UUID
	var project sql.NullString
	var v ArtifactVersion
	err := s.db.QueryRowContext(ctx, `SELECT a.id,a.task_id,a.project_id::text,a.title,a.owner_id,
 v.id,v.artifact_id,v.version,v.filename,v.mime_type,v.size,v.sha256,v.validation_status,v.preview_status,v.created_at,v.storage_key,v.preview_key,v.visible_count
 FROM work_artifacts a JOIN LATERAL (
  SELECT av.*,count(*) OVER () AS visible_count FROM work_artifact_versions av
  WHERE av.artifact_id=a.id AND (a.owner_id=$1 OR EXISTS(
   SELECT 1 FROM work_artifact_publications x JOIN projects p ON p.id=x.project_id AND p.deleted_at IS NULL
   WHERE x.artifact_id=a.id AND x.version_id=av.id
    AND (p.team_id IS NULL OR EXISTS(SELECT 1 FROM team_members tm JOIN teams te ON te.id=tm.team_id WHERE tm.team_id=p.team_id AND tm.user_id=$1 AND te.deleted_at IS NULL))
    AND (p.user_id=$1 OR EXISTS(SELECT 1 FROM project_collaborators c WHERE c.project_id=p.id AND c.user_id=$1 AND c.deleted_at IS NULL))))
  ORDER BY (a.owner_id=$1 AND av.id=a.current_version_id) DESC,av.version DESC LIMIT 1
 ) v ON true WHERE a.id=$2 AND a.deleted_at IS NULL`, uid, aid).Scan(
		&out.ID, &out.TaskID, &project, &out.Title, &owner,
		&v.ID, &v.ArtifactID, &v.Version, &v.Filename, &v.MIMEType, &v.Size, &v.SHA256, &v.ValidationStatus, &v.PreviewStatus, &v.CreatedAt, &v.StorageKey, &v.PreviewKey, &out.VersionCount)
	if errors.Is(err, sql.ErrNoRows) {
		return out, errcode.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if err = s.artifactAccess(ctx, uid, aid, v.ID); err != nil {
		return out, err
	}
	if err = s.prepareArtifactPreview(ctx, &v); err != nil {
		return out, err
	}
	if project.Valid {
		id, _ := uuid.Parse(project.String)
		out.ProjectID = &id
	}
	out.CurrentVersionID, out.CurrentVersion = v.ID, &v
	out.Filename, out.ValidationStatus, out.PreviewStatus = v.Filename, v.ValidationStatus, v.PreviewStatus
	out.CanEdit = owner == uid
	if !out.CanEdit {
		out.TaskID = uuid.Nil
	}
	return out, nil
}

func (s *Service) ListArtifacts(ctx context.Context, uid uuid.UUID, pid, tid *uuid.UUID, req ArtifactPageRequest) (ArtifactPage, error) {
	out := ArtifactPage{Items: []Artifact{}}
	if err := s.enabled(); err != nil {
		return out, err
	}
	if req.Limit == 0 {
		req.Limit = 24
	}
	if req.Limit < 1 || req.Limit > 100 {
		return out, errcode.ErrBadRequest
	}
	cursor, err := decodeArtifactCursor(req.Cursor)
	if err != nil {
		return out, err
	}
	if pid != nil {
		if err = s.projectPermission(ctx, uid, *pid, false); err != nil {
			return out, err
		}
	}
	if tid != nil {
		if _, err = s.taskPermission(ctx, uid, *tid); err != nil {
			return out, err
		}
	}
	var lastVisible artifactCursor
	for {
		var at, id any
		if cursor != nil {
			at, id = cursor.CreatedAt, cursor.ID
		}
		rows, err := s.db.QueryContext(ctx, `SELECT a.id,a.created_at FROM work_artifacts a
 WHERE a.deleted_at IS NULL AND ($3::uuid IS NULL OR a.task_id=$3)
 AND ($4::timestamptz IS NULL OR (a.created_at,a.id)<($4::timestamptz,$5::uuid))
 AND ((a.owner_id=$1 AND $2::uuid IS NULL) OR EXISTS(
  SELECT 1 FROM work_artifact_publications x JOIN projects p ON p.id=x.project_id
  WHERE x.artifact_id=a.id AND ($2::uuid IS NULL OR x.project_id=$2) AND p.deleted_at IS NULL
   AND (p.team_id IS NULL OR EXISTS(SELECT 1 FROM team_members tm JOIN teams te ON te.id=tm.team_id WHERE tm.team_id=p.team_id AND tm.user_id=$1 AND te.deleted_at IS NULL))
   AND (p.user_id=$1 OR EXISTS(SELECT 1 FROM project_collaborators c WHERE c.project_id=p.id AND c.user_id=$1 AND c.deleted_at IS NULL))))
 ORDER BY a.created_at DESC,a.id DESC LIMIT $6`, uid, pid, tid, at, id, req.Limit+1)
		if err != nil {
			return out, err
		}
		batch := []artifactCursor{}
		for rows.Next() {
			var row artifactCursor
			if err = rows.Scan(&row.ID, &row.CreatedAt); err != nil {
				rows.Close()
				return out, err
			}
			batch = append(batch, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
		for _, row := range batch {
			artifact, err := s.artifactSummary(ctx, uid, row.ID)
			if errors.Is(err, errcode.ErrForbidden) || errors.Is(err, errcode.ErrNotFound) {
				continue
			}
			if err != nil {
				return out, err
			}
			if len(out.Items) == req.Limit {
				out.HasMore = true
				out.NextCursor = encodeArtifactCursor(lastVisible)
				return out, nil
			}
			out.Items = append(out.Items, artifact)
			lastVisible = row
		}
		if len(batch) < req.Limit+1 {
			return out, nil
		}
		// Access can be revoked between queries. Continue scanning past denied
		// candidates so they cannot truncate the employee's remaining results.
		cursor = &batch[len(batch)-1]
	}
}
