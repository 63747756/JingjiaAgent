package work

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/63747756/jingjiaagent/backend/biz/agentresource"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
	"io"
	"strings"
	"time"
)

func (s *Service) pinSkills(ctx context.Context, uid, team uuid.UUID, selected, required []string) ([]SkillPin, []*taskflow.AgentResourceAssetRef, error) {
	return (&SkillResolver{ent: s.ent, objects: s.objects, store: s.store}).pinSkills(ctx, uid, team, selected, required)
}
func (s *SkillResolver) pinSkills(ctx context.Context, uid, team uuid.UUID, selected, required []string) ([]SkillPin, []*taskflow.AgentResourceAssetRef, error) {
	scope := agentresource.ScopeFilter{IncludeGlobal: true, TeamID: &team, UserID: &uid, MemberID: &uid}
	if team == uuid.Nil {
		scope.TeamID = nil
	}
	all, err := agentresource.AuthorizedSkills(ctx, s.ent, scope)
	if err != nil {
		return nil, nil, err
	}
	required = append([]string{}, required...)
	for _, row := range all {
		if row.Enabled && row.IsForceDelivery {
			required = append(required, row.ID.String())
		}
	}
	ids := []uuid.UUID{}
	requested := map[uuid.UUID]bool{}
	for _, raw := range append(append([]string{}, selected...), required...) {
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil {
			return nil, nil, errcode.ErrBadRequest.Wrap(fmt.Errorf("Skill 标识无效"))
		}
		if !requested[id] {
			ids = append(ids, id)
		}
		requested[id] = true
	}
	rows := []*db.AgentSkill{}
	if len(requested) > 128 {
		return nil, nil, skillInputError(fmt.Errorf("启用技能超过128个，请在我的技能中停用部分技能"))
	}
	for _, row := range all {
		if requested[row.ID] && row.Enabled {
			rows = append(rows, row)
		}
	}
	pins := []SkillPin{}
	refs := []*taskflow.AgentResourceAssetRef{}
	resolved := map[uuid.UUID]bool{}
	names := map[string]uuid.UUID{}
	var total agentresource.PackageUsage
	packageNames := []string{}
	for _, row := range rows {
		name := strings.ToLower(row.Name)
		if prior, ok := names[name]; ok && prior != row.ID {
			return nil, nil, skillInputError(fmt.Errorf("技能名称冲突：%s，请在我的技能中停用或重命名；系统技能请联系管理员", row.Name))
		}
		names[name] = row.ID
		skill, err := s.ent.AgentSkill.Get(ctx, row.ID)
		if err != nil {
			return nil, nil, err
		}
		if skill.ActiveVersionID == nil {
			if skill.IsForceDelivery {
				return nil, nil, errcode.ErrForbidden.Wrap(fmt.Errorf("强制下发技能 %s 没有可用版本", row.Name))
			}
			return nil, nil, errcode.ErrBadRequest.Wrap(fmt.Errorf("Skill %s 没有可用版本", row.Name))
		}
		if skill.ScopeType == "team" && skill.PublicationState != "published" {
			return nil, nil, errcode.ErrForbidden
		}
		version, err := s.ent.AgentSkillVersion.Get(ctx, *skill.ActiveVersionID)
		if err != nil {
			return nil, nil, err
		}
		if version.ResourceID != skill.ID {
			return nil, nil, errcode.ErrBadRequest.Wrap(fmt.Errorf("Skill 版本归属不匹配"))
		}
		// Materialize and inspect every chosen package before admitting compute.
		// A selected or required skill is never silently skipped.
		body, err := s.objects.GetObject(ctx, version.S3Key)
		if err != nil {
			return nil, nil, errcode.ErrBadRequest.Wrap(fmt.Errorf("Skill %s 文件不可用: %w", row.Name, err))
		}
		data, readErr := io.ReadAll(io.LimitReader(body, 32<<20+1))
		closeErr := body.Close()
		if readErr != nil {
			return nil, nil, readErr
		}
		if closeErr != nil {
			return nil, nil, closeErr
		}
		if len(data) > 32<<20 {
			return nil, nil, errcode.ErrBadRequest.Wrap(fmt.Errorf("Skill %s 文件过大", row.Name))
		}
		usage, err := agentresource.InspectSkillPackage(data)
		if err != nil {
			return nil, nil, errcode.ErrBadRequest.Wrap(fmt.Errorf("Skill %s 文件无效: %w", row.Name, err))
		}
		packageNames = append(packageNames, row.Name)
		if err = total.Add(usage); err != nil {
			return nil, nil, skillInputError(fmt.Errorf("%w；涉及技能：%s。请在我的技能中停用部分技能；系统技能请联系管理员", err, strings.Join(packageNames, "、")))
		}
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		if version.ContentHash != "" && version.ContentHash != digest {
			return nil, nil, errcode.ErrBadRequest.Wrap(fmt.Errorf("Skill %s 摘要校验失败", row.Name))
		}
		url, err := s.objects.PresignGet(ctx, version.S3Key, 24*time.Hour)
		if err != nil {
			return nil, nil, errcode.ErrBadRequest.Wrap(fmt.Errorf("Skill %s 不能下发", row.Name))
		}
		pins = append(pins, SkillPin{SkillID: row.ID, VersionID: version.ID, Name: row.Name, Version: version.Version, SHA256: digest, S3Key: version.S3Key})
		refs = append(refs, &taskflow.AgentResourceAssetRef{Name: row.Name, Version: version.Version, ZipURL: url, SHA256: digest})
		resolved[row.ID] = true
	}
	for id := range requested {
		if !resolved[id] {
			return nil, nil, errcode.ErrForbidden.Wrap(fmt.Errorf("所选或必备 Skill 不可用、未授权或被其他同名版本覆盖: %s", id))
		}
	}
	return pins, refs, nil
}
