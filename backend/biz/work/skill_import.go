package work

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/63747756/jingjiaagent/backend/biz/agentresource"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/middleware"
	"github.com/GoYoko/web"
	"github.com/google/uuid"
)

//go:embed release/manifest.json release/*.zip
var releaseSkills embed.FS

type SkillPackage struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Version        string   `json:"version"`
	SHA256         string   `json:"sha256"`
	Description    string   `json:"description"`
	PreviousSHA256 []string `json:"previous_sha256,omitempty"`
}
type SkillImportRequest struct {
	TeamID     uuid.UUID `json:"team_id" validate:"required"`
	PackageIDs []string  `json:"package_ids" validate:"required"`
}

func skillPackages() ([]SkillPackage, error) {
	data, err := releaseSkills.ReadFile("release/manifest.json")
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Packages []SkillPackage `json:"packages"`
	}
	if err = json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	for _, p := range manifest.Packages {
		if _, err = verifiedReleasePackage(p); err != nil {
			return nil, err
		}
	}
	return manifest.Packages, nil
}
func verifiedReleasePackage(p SkillPackage) ([]byte, error) {
	if p.ID == "" || strings.ContainsAny(p.ID, "/\\.\x00") {
		return nil, fmt.Errorf("invalid release package identifier")
	}
	data, err := releaseSkills.ReadFile("release/" + p.ID + ".zip")
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != p.SHA256 {
		return nil, fmt.Errorf("release package %s digest mismatch", p.ID)
	}
	if err = agentresource.ValidateSkillPackage(data); err != nil {
		return nil, err
	}
	return data, nil
}

// ImportSkills accepts only IDs from the embedded release catalog. No request
// URL, upload content, filename, or caller-provided digest can replace it.
func (s *Service) ImportSkills(ctx context.Context, user *domain.User, req SkillImportRequest) ([]SkillView, error) {
	return s.importSkills(ctx, user, req, false)
}
func (s *Service) importSkills(ctx context.Context, user *domain.User, req SkillImportRequest, automatic bool) ([]SkillView, error) {
	if err := s.enabled(); err != nil {
		return nil, err
	}
	if len(req.PackageIDs) < 1 || len(req.PackageIDs) > 8 {
		return nil, errcode.ErrBadRequest
	}
	if err := s.teamPermission(ctx, user.ID, req.TeamID, !automatic); err != nil {
		return nil, err
	}
	catalog, err := skillPackages()
	if err != nil {
		return nil, err
	}
	available := map[string]SkillPackage{}
	for _, p := range catalog {
		available[p.ID] = p
	}
	packages := []SkillPackage{}
	seen := map[string]bool{}
	for _, id := range req.PackageIDs {
		p, ok := available[id]
		if !ok || seen[id] {
			return nil, errcode.ErrBadRequest
		}
		packages = append(packages, p)
		seen[id] = true
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var tid uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT t.id FROM teams t JOIN team_members m ON m.team_id=t.id WHERE t.id=$1 AND m.user_id=$2 AND ($3 OR m.role='admin') AND t.deleted_at IS NULL FOR UPDATE OF t,m`, req.TeamID, user.ID, automatic).Scan(&tid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errcode.ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	var repo uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT id FROM agent_skill_repos WHERE scope_type='team' AND scope_id=$1 AND source_type='bare' AND NOT is_deleted ORDER BY created_at,id LIMIT 1 FOR UPDATE`, req.TeamID.String()).Scan(&repo)
	if errors.Is(err, sql.ErrNoRows) {
		repo = uuid.New()
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_skill_repos(id,name,scope_type,scope_id,created_by,source_type) VALUES($1,$2,'team',$3,$4,'bare')`, repo, "team-"+req.TeamID.String(), req.TeamID.String(), user.ID)
	}
	if err != nil {
		return nil, err
	}
	ids := []uuid.UUID{}
	for _, p := range packages {
		var id uuid.UUID
		var activeHash sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT s.id,v.content_hash FROM agent_skills s LEFT JOIN agent_skill_versions v ON v.id=s.active_version_id LEFT JOIN work_builtin_skill_sources b ON b.skill_id=s.id AND b.team_id=$3 AND b.package_id=$4 WHERE s.repo_id=$1 AND (b.package_id=$4 OR (s.name=$2 AND NOT s.is_deleted)) ORDER BY (b.package_id IS NOT NULL) DESC LIMIT 1 FOR UPDATE OF s`, repo, p.Name, req.TeamID, p.ID).Scan(&id, &activeHash)
		if errors.Is(err, sql.ErrNoRows) {
			id = uuid.New()
			_, err = tx.ExecContext(ctx, `INSERT INTO agent_skills(id,repo_id,name,description,scope_type,scope_id,created_by,publication_state,enabled) VALUES($1,$2,$3,$4,'team',$5,$6,'published',true)`, id, repo, p.Name, p.Description, req.TeamID.String(), user.ID)
		} else if err == nil && automatic {
			if _, err = tx.ExecContext(ctx, `INSERT INTO work_builtin_skill_sources(team_id,package_id,skill_id) VALUES($1,$2,$3) ON CONFLICT(team_id,package_id) DO NOTHING`, req.TeamID, p.ID, id); err != nil {
				return nil, err
			}
			// Upgrade only a byte-identical previous official release. Preserve
			// administrator edits, IDs, bindings, enablement and display settings.
			if !builtinLayoutUpgrade(p, activeHash) {
				ids = append(ids, id)
				continue
			}
		}
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
		if _, err = tx.ExecContext(ctx, `INSERT INTO work_builtin_skill_sources(team_id,package_id,skill_id) VALUES($1,$2,$3) ON CONFLICT(team_id,package_id) DO NOTHING`, req.TeamID, p.ID, id); err != nil {
			return nil, err
		}
		if activeHash.Valid && activeHash.String == p.SHA256 {
			continue
		}
		data, err := verifiedReleasePackage(p)
		if err != nil {
			return nil, err
		}
		vid := uuid.New()
		key, hash, err := s.storeSkillVersion(ctx, "team-"+req.TeamID.String(), id, vid, data)
		if err != nil {
			return nil, err
		}
		version, err := nextSkillVersion(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		meta, _ := json.Marshal(map[string]any{"description": p.Description, "source_type": "release", "source_label": "内网工作发行包 " + p.Version, "release_package_id": p.ID, "release_sha256": p.SHA256})
		if _, err = tx.ExecContext(ctx, `INSERT INTO agent_skill_versions(id,resource_id,version,s3_key,content_hash,parsed_meta) VALUES($1,$2,$3,$4,$5,$6)`, vid, id, version, key, hash, string(meta)); err != nil {
			return nil, err
		}
		if automatic && activeHash.Valid {
			_, err = tx.ExecContext(ctx, `UPDATE agent_skills SET active_version_id=$2,updated_at=now() WHERE id=$1`, id, vid)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE agent_skills SET active_version_id=$2,description=$3,publication_state='published',updated_at=now() WHERE id=$1`, id, vid, p.Description)
		}
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if automatic {
		return []SkillView{}, nil
	}
	out := []SkillView{}
	for _, id := range ids {
		v, err := s.Skill(ctx, user, id)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func builtinLayoutUpgrade(p SkillPackage, active sql.NullString) bool {
	if !active.Valid || active.String == p.SHA256 {
		return false
	}
	for _, previous := range p.PreviousSHA256 {
		if active.String == previous {
			return true
		}
	}
	return false
}
func (h *Handler) Packages(c *web.Context) error {
	out, err := skillPackages()
	if err != nil {
		return err
	}
	return c.Success(struct {
		Items []SkillPackage `json:"items"`
	}{out})
}
func (h *Handler) Import(c *web.Context, req SkillImportRequest) error {
	out, err := h.service.ImportSkills(c.Request().Context(), middleware.GetUser(c), req)
	if err != nil {
		return err
	}
	return c.Success(struct {
		Items []SkillView `json:"items"`
	}{out})
}
