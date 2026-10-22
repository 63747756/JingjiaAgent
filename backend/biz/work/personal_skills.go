package work

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/63747756/jingjiaagent/backend/biz/agentresource"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
	"io"
	"path"
	"strings"
)

func (s *Service) readSkillPackage(ctx context.Context, key string) ([]byte, error) {
	body, err := s.objects.GetObject(ctx, key)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, (32<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 32<<20 {
		return nil, skillInputError(errors.New("技能压缩包超过32 MiB"))
	}
	return data, agentresource.ValidateSkillPackage(data)
}
func replaceSkillMarkdown(data []byte, content string) ([]byte, error) {
	if err := agentresource.ValidateSkillPackage(data); err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		header := f.FileHeader
		header.SetMode(f.Mode().Perm())
		w, err := zw.CreateHeader(&header)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(path.Base(f.Name), "SKILL.md") {
			_, err = io.WriteString(w, content)
		} else {
			b, e := f.Open()
			if e != nil {
				return nil, e
			}
			_, err = io.Copy(w, b)
			b.Close()
		}
		if err != nil {
			return nil, err
		}
	}
	if err = zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), agentresource.ValidateSkillPackage(out.Bytes())
}

type packageMeta struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tags        []string `yaml:"tags"`
	Version     string   `yaml:"version"`
}

func parseSkillMeta(content string) (packageMeta, error) {
	var m packageMeta
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(content), "\r\n", "\n"), "\n")
	if len(lines) < 3 || lines[0] != "---" {
		return m, skillInputError(errors.New("SKILL.md 需要包含 name、description 的 YAML 说明"))
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return m, errcode.ErrBadRequest
	}
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &m); err != nil {
		return m, errcode.ErrBadRequest
	}
	if err := validateDraft(DraftSkillRequest{Name: m.Name, Description: m.Description, Tags: m.Tags, Content: content}); err != nil {
		return m, err
	}
	return m, nil
}

func draftMarkdown(req DraftSkillRequest) (string, []string, error) {
	content := strings.ReplaceAll(req.Content, "\r\n", "\n")
	meta := map[string]any{}
	body := content
	if strings.HasPrefix(strings.TrimSpace(content), "---\n") {
		content = strings.TrimSpace(content)
		lines := strings.Split(content, "\n")
		end := -1
		for i := 1; i < len(lines); i++ {
			if lines[i] == "---" {
				end = i
				break
			}
		}
		if end < 0 {
			return "", nil, skillInputError(errors.New("SKILL.md 的 YAML 说明未正确结束"))
		}
		if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &meta); err != nil {
			return "", nil, skillInputError(errors.New("SKILL.md 的 YAML 说明无效"))
		}
		body = strings.Join(lines[end+1:], "\n")
	}
	meta["name"] = req.Name
	meta["description"] = req.Description
	if req.Tags != nil {
		meta["tags"] = req.Tags
	}
	raw, err := yaml.Marshal(meta)
	if err != nil {
		return "", nil, err
	}
	result := "---\n" + string(raw) + "---\n" + body
	parsed, err := parseSkillMeta(result)
	if err != nil {
		return "", nil, err
	}
	return result, parsed.Tags, nil
}
func normalizeSkillZip(data []byte) ([]byte, packageMeta, error) {
	var m packageMeta
	if len(data) > 32<<20 {
		return nil, m, skillInputError(errors.New("技能压缩包超过32 MiB"))
	}
	if err := agentresource.ValidateSkillPackage(data); err != nil {
		return nil, m, skillInputError(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, m, err
	}
	var prefix string
	for _, f := range zr.File {
		if strings.EqualFold(path.Base(f.Name), "SKILL.md") {
			prefix = path.Dir(f.Name)
			if prefix == "." {
				prefix = ""
			} else {
				prefix += "/"
			}
			break
		}
	}
	content, err := skillMarkdown(data)
	if err != nil {
		return nil, m, err
	}
	m, err = parseSkillMeta(content)
	if err != nil {
		return nil, m, err
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !strings.HasPrefix(f.Name, prefix) {
			return nil, m, skillInputError(errors.New("压缩包只能包含一个技能目录"))
		}
		name := strings.TrimPrefix(f.Name, prefix)
		if strings.EqualFold(name, "SKILL.md") {
			name = "SKILL.md"
		}
		header := f.FileHeader
		header.Name = name
		header.SetMode(f.Mode().Perm())
		w, e := zw.CreateHeader(&header)
		if e != nil {
			return nil, m, e
		}
		r, e := f.Open()
		if e != nil {
			return nil, m, e
		}
		_, e = io.Copy(w, r)
		r.Close()
		if e != nil {
			return nil, m, e
		}
	}
	if err = zw.Close(); err != nil {
		return nil, m, err
	}
	return out.Bytes(), m, agentresource.ValidateSkillPackage(out.Bytes())
}
func (s *Service) SavePersonalPackage(ctx context.Context, u *domain.User, id uuid.UUID, data []byte, source map[string]any) (SkillView, error) {
	if err := s.enabled(); err != nil {
		return SkillView{}, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return SkillView{}, err
	}
	defer tx.Rollback()
	id, err = s.savePersonalPackageTx(ctx, tx, u, id, data, source)
	if err != nil {
		return SkillView{}, err
	}
	if err = tx.Commit(); err != nil {
		return SkillView{}, err
	}
	return s.Skill(ctx, u, id)
}

// The caller commits the skill and any associated source records together.
func (s *Service) savePersonalPackageTx(ctx context.Context, tx *sql.Tx, u *domain.User, id uuid.UUID, data []byte, source map[string]any) (uuid.UUID, error) {
	data, meta, err := normalizeSkillZip(data)
	if err != nil {
		return uuid.Nil, err
	}
	var owner uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, u.ID).Scan(&owner); err != nil {
		return uuid.Nil, err
	}
	if id == uuid.Nil {
		var repo uuid.UUID
		err = tx.QueryRowContext(ctx, `SELECT id FROM agent_skill_repos WHERE scope_type='user' AND scope_id=$1 AND source_type='bare' AND NOT is_deleted ORDER BY created_at,id LIMIT 1 FOR UPDATE`, u.ID.String()).Scan(&repo)
		if errors.Is(err, sql.ErrNoRows) {
			repo = uuid.New()
			_, err = tx.ExecContext(ctx, `INSERT INTO agent_skill_repos(id,name,scope_type,scope_id,created_by,source_type) VALUES($1,$2,'user',$3,$4,'bare')`, repo, "user-"+u.ID.String(), u.ID.String(), u.ID)
		}
		if err != nil {
			return uuid.Nil, err
		}
		id, err = createPersonalSkill(ctx, tx, u.ID, repo, meta.Name, meta.Description)
	} else {
		var name string
		err = tx.QueryRowContext(ctx, `SELECT name FROM agent_skills WHERE id=$1 AND scope_type='user' AND scope_id=$2 AND NOT is_deleted FOR UPDATE`, id, u.ID.String()).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return uuid.Nil, errcode.ErrNotFound
		}
		if err == nil && name != meta.Name {
			return uuid.Nil, skillInputError(errors.New("新版本技能名称必须与现有技能一致"))
		}
	}
	if err != nil {
		return uuid.Nil, err
	}
	version, err := nextSkillVersion(ctx, tx, id)
	if err != nil {
		return uuid.Nil, err
	}
	vid := uuid.New()
	key, hash, err := s.storeSkillVersion(ctx, "user-"+u.ID.String(), id, vid, data)
	if err != nil {
		return uuid.Nil, err
	}
	if source == nil {
		source = map[string]any{"source_type": "personal", "source_label": "个人技能"}
	}
	source["description"] = meta.Description
	source["tags"] = meta.Tags
	raw, _ := json.Marshal(source)
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_skill_versions(id,resource_id,version,s3_key,content_hash,parsed_meta) VALUES($1,$2,$3,$4,$5,$6)`, vid, id, version, key, hash, string(raw))
	if err != nil {
		return uuid.Nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_skills SET active_version_id=$2,description=$3,updated_at=now() WHERE id=$1`, id, vid, meta.Description)
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func createPersonalSkill(ctx context.Context, tx *sql.Tx, uid, repo uuid.UUID, name, description string) (uuid.UUID, error) {
	var id uuid.UUID
	var deleted bool
	err := tx.QueryRowContext(ctx, `SELECT id,is_deleted FROM agent_skills WHERE repo_id=$1 AND lower(name)=lower($2) FOR UPDATE`, repo, name).Scan(&id, &deleted)
	if err == nil {
		if !deleted {
			return uuid.Nil, skillInputError(errors.New("已有同名个人技能，请编辑并保存新版本"))
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_skills SET is_deleted=false,enabled=true,name=$2,description=$3,updated_at=now() WHERE id=$1`, id, name, description)
		return id, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, err
	}
	id = uuid.New()
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_skills(id,repo_id,name,description,scope_type,scope_id,created_by,publication_state,enabled) VALUES($1,$2,$3,$4,'user',$5,$6,'draft',true)`, id, repo, name, description, uid.String(), uid)
	return id, err
}
func (s *Service) SetPersonalSkillEnabled(ctx context.Context, u *domain.User, id uuid.UUID, enabled *bool) error {
	if enabled == nil {
		return errcode.ErrBadRequest
	}
	result, err := s.store.db.ExecContext(ctx, `UPDATE agent_skills SET enabled=$3,updated_at=now() WHERE id=$1 AND scope_type='user' AND scope_id=$2 AND NOT is_deleted AND (NOT $3 OR active_version_id IS NOT NULL)`, id, u.ID.String(), *enabled)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errcode.ErrNotFound
	}
	return nil
}
func (s *Service) DeletePersonalSkill(ctx context.Context, u *domain.User, id uuid.UUID) error {
	result, err := s.store.db.ExecContext(ctx, `UPDATE agent_skills SET is_deleted=true,enabled=false,updated_at=now() WHERE id=$1 AND scope_type='user' AND scope_id=$2 AND NOT is_deleted`, id, u.ID.String())
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errcode.ErrNotFound
	}
	return nil
}
func archiveUpload(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, (32<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 32<<20 {
		return nil, fmt.Errorf("技能压缩包超过32 MiB")
	}
	return data, nil
}
