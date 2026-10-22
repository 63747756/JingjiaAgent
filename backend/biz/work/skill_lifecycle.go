package work

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/63747756/jingjiaagent/backend/biz/agentresource"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
)

type SkillVersionView struct {
	ID        uuid.UUID `json:"id"`
	Version   string    `json:"version"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
}
type SkillView struct {
	ID              uuid.UUID          `json:"id"`
	Name            string             `json:"name"`
	Description     string             `json:"description"`
	Scope           domain.SkillScope  `json:"scope"`
	State           string             `json:"publication_state"`
	Enabled         bool               `json:"enabled"`
	ActiveVersionID *uuid.UUID         `json:"active_version_id,omitempty"`
	Versions        []SkillVersionView `json:"versions"`
	Content         string             `json:"content,omitempty"`
	Tags            []string           `json:"tags"`
	SourceType      string             `json:"source_type"`
	SourceLabel     string             `json:"source_label"`
	SourceURL       string             `json:"source_url,omitempty"`
	SourceVersion   string             `json:"source_version,omitempty"`
}
type DraftSkillRequest struct {
	Name        string   `json:"name" validate:"required"`
	Description string   `json:"description"`
	Content     string   `json:"content" validate:"required"`
	Tags        []string `json:"tags"`
}
type SkillRollbackRequest struct {
	TeamID    uuid.UUID `json:"team_id" validate:"required"`
	VersionID uuid.UUID `json:"version_id" validate:"required"`
}
type SkillReview struct {
	ID            uuid.UUID  `json:"id"`
	SkillID       uuid.UUID  `json:"skill_id"`
	VersionID     uuid.UUID  `json:"version_id"`
	TeamID        uuid.UUID  `json:"team_id"`
	State         string     `json:"state"`
	Note          string     `json:"note"`
	SharedSkillID *uuid.UUID `json:"shared_skill_id,omitempty"`
	SkillName     string     `json:"skill_name"`
	SubmitterID   uuid.UUID  `json:"submitter_id"`
	Content       string     `json:"content,omitempty"`
	SHA256        string     `json:"sha256,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

func validateDraft(req DraftSkillRequest) error {
	if strings.TrimSpace(req.Name) == "" || len(req.Name) > 120 || req.Name == "." || req.Name == ".." || strings.ContainsAny(req.Name, "/\\\x00\r\n") || strings.TrimSpace(req.Content) == "" || len(req.Content) > 256<<10 || len(req.Description) > 4096 || len(req.Tags) > 20 {
		return errcode.ErrBadRequest
	}
	return nil
}
func zipMarkdown(content string) ([]byte, error) {
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	f, err := z.Create("SKILL.md")
	if err != nil {
		return nil, err
	}
	if _, err = f.Write([]byte(content)); err != nil {
		return nil, err
	}
	if err = z.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func (s *Service) storeSkillVersion(ctx context.Context, scope string, skill, version uuid.UUID, data []byte) (string, string, error) {
	if err := agentresource.ValidateSkillPackage(data); err != nil {
		return "", "", err
	}
	prefix := "work-skills/" + scope + "/" + skill.String()
	filename := version.String() + ".zip"
	if err := s.objects.PutFile(ctx, prefix, filename, bytes.NewReader(data)); err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(data)
	return prefix + "/" + filename, hex.EncodeToString(sum[:]), nil
}
func (s *Service) skillAccessible(ctx context.Context, uid, id uuid.UUID) error {
	var scope, sid, state string
	var enabled bool
	err := s.store.db.QueryRowContext(ctx, `SELECT scope_type,scope_id,enabled,publication_state FROM agent_skills WHERE id=$1 AND NOT is_deleted`, id).Scan(&scope, &sid, &enabled, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return errcode.ErrNotFound
	}
	if err != nil {
		return err
	}
	if scope == "user" {
		if sid != uid.String() {
			return errcode.ErrNotFound
		}
		return nil
	}
	if scope == "team" {
		team, err := uuid.Parse(sid)
		if err != nil {
			return err
		}
		if err = s.teamPermission(ctx, uid, team, false); err != nil {
			return err
		}
		var admin, granted bool
		if err = s.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM team_members WHERE user_id=$1 AND team_id=$2 AND role='admin')`, uid, team).Scan(&admin); err != nil {
			return err
		}
		if admin {
			return nil
		}
		if !enabled || state != "published" {
			return errcode.ErrNotFound
		}
		if err = s.store.db.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM agent_skill_group_bindings WHERE skill_id=$1) OR EXISTS(SELECT 1 FROM agent_skill_group_bindings b JOIN team_groups gr ON gr.id=b.group_id JOIN team_group_members gm ON gm.group_id=gr.id WHERE b.skill_id=$1 AND gr.team_id=$2 AND gr.deleted_at IS NULL AND gm.user_id=$3)`, id, team, uid).Scan(&granted); err != nil {
			return err
		}
		if granted {
			return nil
		}
		return errcode.ErrNotFound
	}
	if scope == "global" && enabled && state == "published" {
		return nil
	}
	return errcode.ErrNotFound
}
func (s *Service) teamPermission(ctx context.Context, uid, tid uuid.UUID, admin bool) error {
	if tid == uuid.Nil {
		return errcode.ErrBadRequest
	}
	var allowed bool
	err := s.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM team_members m JOIN teams t ON t.id=m.team_id WHERE m.user_id=$1 AND m.team_id=$2 AND t.deleted_at IS NULL AND (NOT $3 OR m.role='admin'))`, uid, tid, admin).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return errcode.ErrForbidden
	}
	return nil
}
func (s *Service) Skill(ctx context.Context, user *domain.User, id uuid.UUID) (SkillView, error) {
	return s.skill(ctx, user, id, true)
}
func (s *Service) skill(ctx context.Context, user *domain.User, id uuid.UUID, includeContent bool) (SkillView, error) {
	if err := s.skillAccessible(ctx, user.ID, id); err != nil {
		return SkillView{}, err
	}
	var out SkillView
	var active sql.NullString
	var scope, sid string
	err := s.store.db.QueryRowContext(ctx, `SELECT id,name,COALESCE(description, ''),scope_type,scope_id,publication_state,enabled,active_version_id::text FROM agent_skills WHERE id=$1 AND NOT is_deleted`, id).Scan(&out.ID, &out.Name, &out.Description, &scope, &sid, &out.State, &out.Enabled, &active)
	if err != nil {
		return out, err
	}
	out.Scope = domain.SkillScope{Type: scope, ID: sid}
	out.Tags = []string{}
	out.SourceType = "personal"
	out.Versions = []SkillVersionView{}
	if active.Valid {
		v, err := uuid.Parse(active.String)
		if err != nil {
			return out, err
		}
		out.ActiveVersionID = &v
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT id,version,content_hash,created_at,s3_key FROM agent_skill_versions WHERE resource_id=$1 ORDER BY created_at DESC,id DESC`, id)
	if err != nil {
		return out, err
	}
	var activeKey string
	for rows.Next() {
		var v SkillVersionView
		var key string
		if err = rows.Scan(&v.ID, &v.Version, &v.SHA256, &v.CreatedAt, &key); err != nil {
			rows.Close()
			return out, err
		}
		out.Versions = append(out.Versions, v)
		if out.ActiveVersionID != nil && v.ID == *out.ActiveVersionID {
			activeKey = key
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if activeKey != "" {
		var meta []byte
		if err = s.store.db.QueryRowContext(ctx, `SELECT parsed_meta FROM agent_skill_versions WHERE id=$1`, out.ActiveVersionID).Scan(&meta); err != nil {
			return out, err
		}
		var m struct {
			Tags             []string `json:"tags"`
			SourceType       string   `json:"source_type"`
			SourceLabel      string   `json:"source_label"`
			SourceURL        string   `json:"source_url"`
			ReleasePackageID string   `json:"release_package_id"`
			RemoteVersion    string   `json:"remote_version"`
		}
		if err = json.Unmarshal(meta, &m); err != nil {
			return out, err
		}
		if m.Tags != nil {
			out.Tags = m.Tags
		}
		out.SourceLabel = m.SourceLabel
		out.SourceURL = m.SourceURL
		out.SourceVersion = m.RemoteVersion
		if m.SourceType == "market" && s.cfg != nil && s.cfg.Work.SkillHub.URL != "" {
			var registry, namespace, slug string
			err = s.store.db.QueryRowContext(ctx, `SELECT registry_url,namespace,slug FROM work_skillhub_installs WHERE user_id=$1 AND skill_id=$2`, user.ID, id).Scan(&registry, &namespace, &slug)
			if err == nil && registry == strings.TrimRight(s.cfg.Work.SkillHub.URL, "/") {
				if h, e := s.hub(ctx); e == nil {
					out.SourceURL = h.publicEndpoint("/space/" + namespace + "/" + slug)
				}
			} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return out, err
			}
		}
		if m.SourceType != "" {
			out.SourceType = m.SourceType
		}
		if m.ReleasePackageID != "" || m.SourceType == "release" {
			out.SourceType = "builtin"
		}
		if !includeContent {
			return out, nil
		}
		body, err := s.objects.GetObject(ctx, activeKey)
		if err != nil {
			return out, err
		}
		data, err := io.ReadAll(io.LimitReader(body, (32<<20)+1))
		body.Close()
		if err != nil {
			return out, err
		}
		out.Content, err = skillMarkdown(data)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
func skillMarkdown(data []byte) (string, error) {
	if err := agentresource.ValidateSkillPackage(data); err != nil {
		return "", err
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	for _, f := range r.File {
		if strings.EqualFold(path.Base(f.Name), "SKILL.md") {
			body, err := f.Open()
			if err != nil {
				return "", err
			}
			b, err := io.ReadAll(io.LimitReader(body, (256<<10)+1))
			body.Close()
			if err != nil {
				return "", err
			}
			if len(b) > 256<<10 {
				return "", errcode.ErrBadRequest
			}
			return string(b), nil
		}
	}
	return "", errcode.ErrBadRequest
}
func (s *Service) Skills(ctx context.Context, user *domain.User, teamID *uuid.UUID) ([]SkillView, error) {
	if teamID != nil {
		if err := s.teamPermission(ctx, user.ID, *teamID, false); err != nil {
			return nil, err
		}
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT id FROM agent_skills WHERE NOT is_deleted AND ((scope_type='user' AND scope_id=$1) OR (scope_type='global' AND publication_state='published') OR ($2::text IS NOT NULL AND scope_type='team' AND scope_id=$2 AND publication_state='published')) ORDER BY updated_at DESC,id DESC`, user.ID.String(), teamID)
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
	out := []SkillView{}
	for _, id := range ids {
		view, err := s.skill(ctx, user, id, false)
		if errors.Is(err, errcode.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		view.Content = ""
		out = append(out, view)
	}
	return out, nil
}
func (s *Service) SaveDraft(ctx context.Context, user *domain.User, id uuid.UUID, req DraftSkillRequest) (SkillView, error) {
	if err := s.enabled(); err != nil {
		return SkillView{}, err
	}
	req.Name = strings.TrimSpace(req.Name)
	if err := validateDraft(req); err != nil {
		return SkillView{}, err
	}
	content, tags, err := draftMarkdown(req)
	if err != nil {
		return SkillView{}, err
	}
	req.Content = content
	req.Tags = tags
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return SkillView{}, err
	}
	defer tx.Rollback()
	var owner uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, user.ID).Scan(&owner); err != nil {
		return SkillView{}, err
	}
	if id == uuid.Nil {
		var repo uuid.UUID
		err = tx.QueryRowContext(ctx, `SELECT id FROM agent_skill_repos WHERE scope_type='user' AND scope_id=$1 AND source_type='bare' AND NOT is_deleted ORDER BY created_at,id LIMIT 1 FOR UPDATE`, user.ID.String()).Scan(&repo)
		if errors.Is(err, sql.ErrNoRows) {
			repo = uuid.New()
			_, err = tx.ExecContext(ctx, `INSERT INTO agent_skill_repos(id,name,scope_type,scope_id,created_by,source_type) VALUES($1,$2,'user',$3,$4,'bare')`, repo, "user-"+user.ID.String(), user.ID.String(), user.ID)
		}
		if err != nil {
			return SkillView{}, err
		}
		id, err = createPersonalSkill(ctx, tx, user.ID, repo, req.Name, req.Description)
		if err != nil {
			return SkillView{}, err
		}
	} else {
		var name string
		if err = tx.QueryRowContext(ctx, `SELECT name FROM agent_skills WHERE id=$1 AND scope_type='user' AND scope_id=$2 AND NOT is_deleted FOR UPDATE`, id, user.ID.String()).Scan(&name); errors.Is(err, sql.ErrNoRows) {
			return SkillView{}, errcode.ErrNotFound
		} else if err != nil {
			return SkillView{}, err
		}
		if name != req.Name {
			return SkillView{}, errcode.ErrBadRequest.Wrap(errors.New("已有 Skill 名称不可变更"))
		}
	}
	version, err := nextSkillVersion(ctx, tx, id)
	if err != nil {
		return SkillView{}, err
	}
	data, err := zipMarkdown(req.Content)
	if err != nil {
		return SkillView{}, err
	}
	versionMeta := map[string]any{"source_type": "personal", "source_label": "个人技能"}
	if id != uuid.Nil {
		var key sql.NullString
		var oldMeta []byte
		if err = tx.QueryRowContext(ctx, `SELECT v.s3_key,COALESCE(v.parsed_meta,'{}'::jsonb) FROM agent_skills s LEFT JOIN agent_skill_versions v ON v.id=s.active_version_id WHERE s.id=$1`, id).Scan(&key, &oldMeta); err != nil {
			return SkillView{}, err
		}
		if key.Valid {
			if err = json.Unmarshal(oldMeta, &versionMeta); err != nil {
				return SkillView{}, err
			}
			original, err := s.readSkillPackage(ctx, key.String)
			if err != nil {
				return SkillView{}, err
			}
			data, err = replaceSkillMarkdown(original, req.Content)
			if err != nil {
				return SkillView{}, err
			}
		}
	}
	vid := uuid.New()
	key, hash, err := s.storeSkillVersion(ctx, "user-"+user.ID.String(), id, vid, data)
	if err != nil {
		return SkillView{}, err
	}
	versionMeta["description"] = req.Description
	if req.Tags != nil {
		versionMeta["tags"] = req.Tags
	}
	meta, _ := json.Marshal(versionMeta)
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_skill_versions(id,resource_id,version,s3_key,content_hash,parsed_meta) VALUES($1,$2,$3,$4,$5,$6)`, vid, id, version, key, hash, string(meta)); err != nil {
		return SkillView{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_skills SET active_version_id=$2,description=$3,publication_state='draft',updated_at=now() WHERE id=$1`, id, vid, req.Description); err != nil {
		return SkillView{}, err
	}
	if err = tx.Commit(); err != nil {
		return SkillView{}, err
	}
	return s.Skill(ctx, user, id)
}
func nextSkillVersion(ctx context.Context, tx *sql.Tx, id uuid.UUID) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version FROM agent_skill_versions WHERE resource_id=$1`, id)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	max := 0
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return "", err
		}
		n, e := strconv.Atoi(strings.TrimPrefix(raw, "v"))
		if e == nil && n > max {
			max = n
		}
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	return fmt.Sprintf("v%d", max+1), nil
}

func (s *Service) Reviews(ctx context.Context, user *domain.User, team uuid.UUID) ([]SkillReview, error) {
	if err := s.teamPermission(ctx, user.ID, team, true); err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT r.id,r.skill_id,r.version_id,r.team_id,r.state,r.note,r.shared_skill_id::text,s.name,r.submitter_id,v.content_hash,r.created_at FROM work_skill_reviews r JOIN agent_skills s ON s.id=r.skill_id JOIN agent_skill_versions v ON v.id=r.version_id WHERE r.team_id=$1 ORDER BY r.created_at DESC LIMIT 100`, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SkillReview{}
	for rows.Next() {
		var r SkillReview
		var shared sql.NullString
		if err = rows.Scan(&r.ID, &r.SkillID, &r.VersionID, &r.TeamID, &r.State, &r.Note, &shared, &r.SkillName, &r.SubmitterID, &r.SHA256, &r.CreatedAt); err != nil {
			return nil, err
		}
		if shared.Valid {
			id, err := uuid.Parse(shared.String)
			if err != nil {
				return nil, err
			}
			r.SharedSkillID = &id
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) ReviewDetail(ctx context.Context, user *domain.User, rid, team uuid.UUID) (SkillReview, error) {
	if err := s.teamPermission(ctx, user.ID, team, true); err != nil {
		return SkillReview{}, err
	}
	var out SkillReview
	var key string
	err := s.store.db.QueryRowContext(ctx, `SELECT r.id,r.skill_id,r.version_id,r.team_id,r.state,r.note,s.name,r.submitter_id,v.content_hash,v.s3_key,r.created_at FROM work_skill_reviews r JOIN agent_skills s ON s.id=r.skill_id JOIN agent_skill_versions v ON v.id=r.version_id AND v.resource_id=r.skill_id WHERE r.id=$1 AND r.team_id=$2`, rid, team).Scan(&out.ID, &out.SkillID, &out.VersionID, &out.TeamID, &out.State, &out.Note, &out.SkillName, &out.SubmitterID, &out.SHA256, &key, &out.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return out, errcode.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	body, err := s.objects.GetObject(ctx, key)
	if err != nil {
		return out, err
	}
	data, err := io.ReadAll(io.LimitReader(body, (32<<20)+1))
	body.Close()
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(data)
	if out.SHA256 != "" && out.SHA256 != hex.EncodeToString(sum[:]) {
		return out, errcode.ErrBadRequest.Wrap(errors.New("待审核版本摘要验证失败"))
	}
	out.Content, err = skillMarkdown(data)
	return out, err
}
func (s *Service) RollbackSkill(ctx context.Context, user *domain.User, id uuid.UUID, req SkillRollbackRequest) (SkillView, error) {
	if err := s.enabled(); err != nil {
		return SkillView{}, err
	}
	if req.VersionID == uuid.Nil {
		return SkillView{}, errcode.ErrBadRequest
	}
	if err := s.teamPermission(ctx, user.ID, req.TeamID, true); err != nil {
		return SkillView{}, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return SkillView{}, err
	}
	defer tx.Rollback()
	var current, target uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT active_version_id FROM agent_skills WHERE id=$1 AND scope_type='team' AND scope_id=$2 AND NOT is_deleted AND publication_state='published' FOR UPDATE`, id, req.TeamID.String()).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return SkillView{}, errcode.ErrNotFound
	}
	if err != nil {
		return SkillView{}, err
	}
	var key, hash string
	if err = tx.QueryRowContext(ctx, `SELECT id,s3_key,content_hash FROM agent_skill_versions WHERE id=$1 AND resource_id=$2`, req.VersionID, id).Scan(&target, &key, &hash); err != nil {
		return SkillView{}, errcode.ErrNotFound
	}
	body, err := s.objects.GetObject(ctx, key)
	if err != nil {
		return SkillView{}, err
	}
	data, err := io.ReadAll(io.LimitReader(body, (32<<20)+1))
	body.Close()
	if err != nil {
		return SkillView{}, err
	}
	sum := sha256.Sum256(data)
	if hash != "" && hash != hex.EncodeToString(sum[:]) {
		return SkillView{}, errcode.ErrBadRequest.Wrap(errors.New("回滚版本摘要验证失败"))
	}
	if err = agentresource.ValidateSkillPackage(data); err != nil {
		return SkillView{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_skills SET active_version_id=$2,updated_at=now() WHERE id=$1`, id, target); err != nil {
		return SkillView{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO work_skill_rollbacks(id,skill_id,from_version_id,to_version_id,team_id,operator_id) VALUES($1,$2,$3,$4,$5,$6)`, uuid.New(), id, current, target, req.TeamID, user.ID); err != nil {
		return SkillView{}, err
	}
	if err = tx.Commit(); err != nil {
		return SkillView{}, err
	}
	return s.Skill(ctx, user, id)
}
