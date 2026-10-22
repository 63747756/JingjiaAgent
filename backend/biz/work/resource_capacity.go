package work

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/63747756/jingjiaagent/backend/biz/agentresource"
	"github.com/63747756/jingjiaagent/backend/db/agentplugin"
	"github.com/63747756/jingjiaagent/backend/db/agentpluginversion"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

// Skills have already been validated by pinSkills. When plugins are present,
// account for both kinds using the exact persisted packages, never a remote URL.
func (r *SkillResolver) ValidateRoundResources(ctx context.Context, task *taskflow.CreateTaskReq) error {
	if task.RoundSkills == nil || task.AgentResources == nil || len(task.AgentResources.Plugins) == 0 {
		return nil
	}
	if len(task.AgentResources.Plugins) > 128 {
		return skillInputError(fmt.Errorf("本轮插件超过128个，请联系管理员调整插件配置"))
	}
	var total agentresource.PackageUsage
	names := []string{}
	add := func(key, name, expected string) (string, error) {
		if key == "" {
			return "", skillInputError(fmt.Errorf("资源 %s 缺少文件信息，请联系管理员", name))
		}
		body, err := r.objects.GetObject(ctx, key)
		if err != nil {
			return "", skillInputError(fmt.Errorf("资源 %s 文件不可用，请联系管理员", name))
		}
		data, err := io.ReadAll(io.LimitReader(body, (32<<20)+1))
		closeErr := body.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
		if len(data) > 32<<20 {
			return "", skillInputError(fmt.Errorf("资源 %s 压缩包超过32 MiB", name))
		}
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		if expected != "" && expected != digest {
			return "", skillInputError(fmt.Errorf("资源 %s 摘要校验失败，请联系管理员", name))
		}
		usage, err := agentresource.InspectResourcePackage(data)
		if err != nil {
			return "", skillInputError(fmt.Errorf("资源 %s 文件无效：%w", name, err))
		}
		names = append(names, name)
		if err = total.Add(usage); err != nil {
			return "", skillInputError(fmt.Errorf("%w；涉及资源：%s。请在我的技能中停用部分技能；系统技能或插件请联系管理员", err, strings.Join(names, "、")))
		}
		return digest, nil
	}
	for _, pin := range task.RoundSkills.Pins {
		if _, err := add(pin.S3Key, pin.Name, pin.SHA256); err != nil {
			return err
		}
	}
	for _, ref := range task.AgentResources.Plugins {
		if ref == nil {
			return skillInputError(fmt.Errorf("插件配置无效，请联系管理员"))
		}
		key := ref.S3Key
		if key == "" {
			var err error
			key, err = r.legacyPluginKey(ctx, ref)
			if err != nil {
				return err
			}
		}
		digest, err := add(key, ref.Name, ref.SHA256)
		if err != nil {
			return err
		}
		ref.S3Key, ref.SHA256 = key, digest
	}
	return nil
}

// Old task intents only stored a presigned URL. Recover the key from immutable
// version records and an exact URL path suffix; never fetch a caller's URL or
// silently substitute the plugin's current active version.
func (r *SkillResolver) legacyPluginKey(ctx context.Context, ref *taskflow.AgentResourceAssetRef) (string, error) {
	u, err := url.Parse(ref.ZipURL)
	if err != nil {
		return "", skillInputError(fmt.Errorf("插件 %s 文件地址无效", ref.Name))
	}
	versions, err := r.ent.AgentPluginVersion.Query().Where(
		agentpluginversion.VersionEQ(ref.Version),
		agentpluginversion.HasPluginWith(agentplugin.NameEQ(ref.Name)),
	).All(ctx)
	if err != nil {
		return "", err
	}
	key := ""
	for _, version := range versions {
		if version.S3Key != "" && strings.HasSuffix(u.Path, "/"+strings.TrimPrefix(version.S3Key, "/")) {
			if key != "" && key != version.S3Key {
				return "", skillInputError(fmt.Errorf("插件 %s 历史版本不明确，请联系管理员", ref.Name))
			}
			key = version.S3Key
		}
	}
	if key == "" {
		return "", skillInputError(fmt.Errorf("插件 %s 历史文件不可用，请联系管理员", ref.Name))
	}
	return key, nil
}
