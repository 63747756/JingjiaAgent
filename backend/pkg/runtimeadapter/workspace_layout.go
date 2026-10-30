package runtimeadapter

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

//go:embed guest/workspace_layout.py
var workspaceLayoutScript []byte

const workspaceNotice = "当前平台工作目录约定（编码和办公任务共用）：仓库与源码在 /workspace；平台中间材料保存到 /workspace/.jingjiaagent/work，正式交付物保存到 /workspace/.jingjiaagent/output。内置办公技能中的 work/、output/ 简称指这两个平台目录。上传附件按实际路径读取并保留原件，平台不预设 input 或 project 目录。源码、构建目录和用户指定的业务路径按仓库及用户要求处理。首次根据原始资料制作成果时创建正式成果文件；用户修改已有成果时更新对应成果文件，历史版本由平台保存，除非用户要求，不另建版本后缀文件。工具拒绝覆盖时先在中间目录生成并检查，再替换对应正式成果，不能覆盖上传原件。\n依赖优先复用；缺包时按 /opt/jingjiaagent/dependencies/README.md 安装，遵循项目明确的包源与锁文件，项目未配置时默认使用 Studio Nexus 内网源。Python 新增依赖使用项目或任务虚拟环境，Node 依赖安装到项目目录，不修改预装办公环境；源不可达或缺版本时说明具体缺口，不擅自更换项目包源。"

func (c *Client) prepareWorkspace(ctx context.Context, env Environment, task taskflow.CreateTaskReq, engine *Engine) error {
	patchOpenCode := task.CodingAgent == taskflow.CodingAgentOpenCode
	for _, file := range task.Configs {
		if strings.HasSuffix(guestConfigPath(file.Path, "${HOME}"), "/.config/opencode/opencode.json") {
			patchOpenCode = false
		}
	}
	data := mustJSON(map[string]any{"patch_opencode": patchOpenCode, "workspace_notice": workspaceNotice})
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
		return connect.NewError(connect.CodeInvalidArgument, errors.New("工作目录初始化失败，请检查目录冲突、链接或文件完整性"))
	}
	return nil
}

// Keep platform guidance in its own rule, never in the user's message or a
// team's rule file. Replacing this managed slot keeps repeated updates stable.
func workspaceTaskConfigs(task taskflow.CreateTaskReq) taskflow.CreateTaskReq {
	task.Configs = append([]taskflow.ConfigFile{}, task.Configs...)
	rule := "${HOME}/.codingmatrix/project-tpl/.ai-ready/rules/jingjiaagent-workspace.md"
	found := false
	for i, file := range task.Configs {
		candidate := strings.Replace(guestConfigPath(file.Path, "${HOME}"), "~/", "${HOME}/", 1)
		if candidate == rule {
			found = true
			task.Configs[i].Content = workspaceNotice
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
