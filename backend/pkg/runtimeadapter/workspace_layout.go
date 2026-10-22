package runtimeadapter

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"strings"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

//go:embed guest/workspace_layout.py
var workspaceLayoutScript []byte

const workspaceNotice = "当前平台工作目录约定（编码和办公任务共用）：仓库与源码仍在 /workspace；平台中间材料保存到 /workspace/.jingjiaagent/work，正式交付物保存到 /workspace/.jingjiaagent/output。内置办公技能中的 work/、output/ 简称指这两个平台目录。旧对话中的根目录 work/output 是历史位置，新的平台文件使用当前目录。上传附件按实际路径读取，平台不预设 input 或 project 目录。源码、构建目录和用户指定的业务路径按仓库及用户要求处理，不因平台约定改变。用户要求修改已有成果时更新对应成果文件，历史版本由平台保存，不受旧平台提示中禁止覆盖同名成果的限制；上传的原始附件仍应保留。"

func (c *Client) prepareWorkspace(ctx context.Context, env Environment, task taskflow.CreateTaskReq, engine *Engine) error {
	legacy := task.Env["JINGJIAAGENT_WORK_MODE"] == "1" && strings.Contains(task.SystemPrompt, "/workspace/work") && strings.Contains(task.SystemPrompt, "/workspace/output")
	files := []string{}
	if legacy && env.Request.Git.URL != "" {
		var raw []byte
		err := c.ledger.db.QueryRowContext(ctx, `SELECT manifest FROM workspace_checkpoints WHERE task_id=$1 AND environment_id=$2 AND state='committed' ORDER BY generation DESC,created_at DESC LIMIT 1`, task.ID, env.ID).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			var snapshot struct {
				Files []struct {
					Path string `json:"path"`
				} `json:"files"`
			}
			if err = json.Unmarshal(raw, &snapshot); err != nil {
				return err
			}
			for _, file := range snapshot.Files {
				files = append(files, file.Path)
			}
		}
	}
	patchOpenCode := task.CodingAgent == taskflow.CodingAgentOpenCode
	for _, file := range task.Configs {
		if strings.HasSuffix(guestConfigPath(file.Path, "${HOME}"), "/.config/opencode/opencode.json") {
			patchOpenCode = false
		}
	}
	data := mustJSON(map[string]any{"migrate_legacy": legacy, "legacy_files": files, "patch_opencode": patchOpenCode, "workspace_notice": workspaceNotice})
	if len(data) > 64000 {
		// Large checkpoint inventories use the existing bounded upload protocol,
		// not a shell argument that can exceed Linux's per-argument limit.
		filename := "/data/state/jingjiaagent-layout/" + task.ID.String() + ".json"
		chunks := make(chan []byte, 1)
		chunks <- data
		close(chunks)
		mode := uint32(0600)
		if err := (&fileClient{c}).upload(ctx, taskflow.FileReq{ID: env.ID, UserID: env.OwnerID, Path: filename}, chunks, &mode, true); err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		data = mustJSON(map[string]string{"request_path": filename, "sha256": hex.EncodeToString(digest[:])})
	}
	command := "python3 -c \"import base64;exec(base64.b64decode('" + base64.StdEncoding.EncodeToString(workspaceLayoutScript) + "'))\" '" + base64.StdEncoding.EncodeToString(data) + "'"
	text, err := engine.execute(ctx, env.SandboxID, command, 4096)
	if err != nil {
		return err
	}
	var result struct {
		Error string `json:"error"`
		Data  struct {
			Installed bool `json:"installed"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(text), &result) != nil || result.Error != "" || !result.Data.Installed {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("工作目录初始化或迁移失败，请检查目录冲突、链接或文件完整性"))
	}
	return nil
}

// Add the rule to the execution copy, never to the user's message or saved
// assistant configuration. Reuse an existing rule slot when one is available.
func workspaceTaskConfigs(task taskflow.CreateTaskReq) taskflow.CreateTaskReq {
	task.Configs = append([]taskflow.ConfigFile{}, task.Configs...)
	rule := "${HOME}/.codingmatrix/project-tpl/.ai-ready/rules/jingjiaagent-workspace.md"
	found := false
	for i, file := range task.Configs {
		candidate := strings.Replace(guestConfigPath(file.Path, "${HOME}"), "~/", "${HOME}/", 1)
		if path.Dir(candidate) == "${HOME}/.codingmatrix/project-tpl/.ai-ready/rules" && strings.HasSuffix(candidate, ".md") {
			rule, found = file.Path, true
			if !strings.Contains(file.Content, workspaceNotice) {
				task.Configs[i].Content += "\n\n" + workspaceNotice
			}
			break
		}
	}
	if !found {
		task.Configs = append(task.Configs, taskflow.ConfigFile{Path: rule, Content: workspaceNotice})
	}
	if task.CodingAgent == taskflow.CodingAgentOpenCode {
		for i, file := range task.Configs {
			if strings.HasSuffix(guestConfigPath(file.Path, "${HOME}"), "/.config/opencode/opencode.json") {
				var cfg map[string]any
				if json.Unmarshal([]byte(file.Content), &cfg) == nil && cfg != nil {
					instructions, _ := cfg["instructions"].([]any)
					canonical := func(value string) string {
						return strings.Replace(guestConfigPath(value, "${HOME}"), "~/", "${HOME}/", 1)
					}
					present := false
					for _, instruction := range instructions {
						if value, ok := instruction.(string); ok && (canonical(value) == canonical(rule) || canonical(value) == "${HOME}/.codingmatrix/project-tpl/.ai-ready/rules/*.md") {
							present = true
						}
					}
					if !present {
						instructions = append(instructions, canonical(rule))
					}
					cfg["instructions"] = instructions
					task.Configs[i].Content = string(mustJSON(cfg))
				}
			}
		}
		// Without a supplied config, prepareWorkspace patches the retained
		// Guest config in place so a model-only change keeps plugins/providers.
	}
	return task
}
