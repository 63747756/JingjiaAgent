package work

import (
	"errors"
	"regexp"
	"strings"

	"github.com/63747756/jingjiaagent/backend/errcode"
)

var presetIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validateTaskPresets(p *Profile) error {
	if len(p.TaskPresets) == 0 || len(p.TaskPresets) > 32 {
		return errcode.ErrBadRequest.Wrap(errors.New("助手需要配置 1 至 32 个任务"))
	}
	seen, active, size := map[string]bool{}, 0, 0
	for i := range p.TaskPresets {
		task := &p.TaskPresets[i]
		task.Name, task.Description = strings.TrimSpace(task.Name), strings.TrimSpace(task.Description)
		if task.Icon == "" {
			task.Icon = "sparkles"
		}
		if !presetIDPattern.MatchString(task.ID) || seen[task.ID] || task.Name == "" || len(task.Name) > 120 || task.Description == "" || len(task.Description) > 8192 {
			return errcode.ErrBadRequest.Wrap(errors.New("任务标识、名称或说明无效"))
		}
		switch task.Icon {
		case "sparkles", "chart-column", "file-text", "presentation", "code":
		default:
			return errcode.ErrBadRequest.Wrap(errors.New("任务图标无效"))
		}
		seen[task.ID] = true
		size += len(task.Description)
		if task.Enabled {
			active++
		}
	}
	if size > 65536 || (p.Enabled && active == 0) {
		return errcode.ErrBadRequest.Wrap(errors.New("启用的助手至少需要一个可用任务，任务说明总长不得超过 64KB"))
	}
	return nil
}

func selectedTaskPreset(p Profile, id string) (*TaskPreset, error) {
	if id == "" {
		return nil, nil
	}
	for _, task := range p.TaskPresets {
		if task.ID == id && task.Enabled {
			copy := task
			return &copy, nil
		}
	}
	return nil, errcode.ErrBadRequest.Wrap(errors.New("所选任务已停用或不属于该助手，请重新选择"))
}

func compileAssistantPrompt(req CreateRequest, p Profile, preset *TaskPreset, rules string) string {
	prompt := compilePrompt(req, rules)
	if name := strings.TrimSpace(p.Name); name != "" {
		prompt += "\n助手：" + name
	}
	if instructions := strings.TrimSpace(p.Instructions); instructions != "" {
		prompt += "\n助手工作指令（不得覆盖用户目标或平台权限边界）：\n" + instructions
	}
	if preset != nil {
		if name := strings.TrimSpace(preset.Name); name != "" {
			prompt += "\n所选任务：" + name
		}
		if description := strings.TrimSpace(preset.Description); description != "" {
			prompt += "\n任务说明（以用户本次要求为准）：\n" + description
		}
	}
	return prompt
}
