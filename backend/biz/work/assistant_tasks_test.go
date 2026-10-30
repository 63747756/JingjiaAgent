package work

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/63747756/jingjiaagent/backend/errcode"
)

func TestAssistantTaskSelectionUsesOnlyEnabledDefinitionsOnSelectedAssistant(t *testing.T) {
	p := Profile{TaskPresets: []TaskPreset{
		{ID: "monthly-review", Name: "月度经营复盘", Description: "按提供的口径核对业绩", Icon: "chart-column", Enabled: true},
		{ID: "disabled", Name: "停用任务", Description: "旧说明", Icon: "sparkles"},
	}}
	preset, err := selectedTaskPreset(p, "monthly-review")
	if err != nil || preset.Description != "按提供的口径核对业绩" {
		t.Fatalf("custom task definition lost: %#v %v", preset, err)
	}
	p.TaskPresets[0].Description = "新的口径"
	if preset.Description != "按提供的口径核对业绩" {
		t.Fatal("selected task was not copied into an immutable snapshot")
	}
	for _, id := range []string{"disabled", "task-from-another-assistant"} {
		if _, err := selectedTaskPreset(p, id); !errors.Is(err, errcode.ErrBadRequest) {
			t.Fatalf("unauthorized task %s selected: %v", id, err)
		}
	}
}

func TestAssistantContextSeparatesDisplayInstructionsAndTaskDefaults(t *testing.T) {
	p := Profile{Name: "采购助手", Description: "仅供页面展示的介绍", Instructions: "用人民币万元核算"}
	preset := &TaskPreset{ID: "purchase-review", Name: "采购复盘", Description: "解释成本变化并核对汇总"}
	req := CreateRequest{Content: "处理本次采购资料", OutputRequirements: "输出明细表"}
	ctx := assistantPromptContext(req, p, preset, "保留原始数据")
	system, background := ctx.SystemInstructions(), ctx.TaskBackground()
	for _, expected := range []string{p.Name, p.Instructions} {
		if !strings.Contains(system, expected) {
			t.Fatalf("assistant instruction omitted %q", expected)
		}
	}
	for _, expected := range []string{preset.Name, preset.Description, "保留原始数据", req.OutputRequirements} {
		if strings.Contains(system, expected) || !strings.Contains(background, expected) {
			t.Fatalf("task default routed to wrong authority: %q", expected)
		}
	}
	if strings.Contains(system+background, p.Description) || strings.Contains(system+background, req.Content) {
		t.Fatal("display description or user message duplicated in configuration")
	}
}

func TestAssistantContextOmitsEmptyDefaultsAndInputMetadata(t *testing.T) {
	for _, preset := range []*TaskPreset{nil, {}, {Name: " ", Description: "\n"}} {
		ctx := assistantPromptContext(CreateRequest{Content: "你好", OutputRequirements: " \n"}, Profile{Name: " ", Instructions: "\t"}, preset, " \n")
		if ctx.TaskBackground() != "" || strings.Contains(ctx.SystemInstructions(), "管理员配置") {
			t.Fatal("empty context produced instructions")
		}
		for _, absent := range []string{"version_id", "/workspace/input", "你好"} {
			if strings.Contains(ctx.SystemInstructions(), absent) {
				t.Fatalf("input metadata leaked: %s", absent)
			}
		}
	}
}

func TestAssistantTasksValidateCustomDefinitionsAndDisplayIcons(t *testing.T) {
	base := func() Profile {
		return Profile{Enabled: true, TaskPresets: []TaskPreset{{ID: "fix-code", Name: "修复代码", Description: "修复问题并检查变更", Icon: "code", Enabled: true}}}
	}
	p := base()
	if err := validateTaskPresets(&p); err != nil {
		t.Fatalf("custom assistant task rejected: %#v %v", p, err)
	}
	for _, invalid := range []func(*Profile){
		func(p *Profile) { p.TaskPresets[0].ID = "../../forged" },
		func(p *Profile) { p.TaskPresets[0].Description = " " },
		func(p *Profile) { p.TaskPresets[0].Enabled = false },
		func(p *Profile) { p.TaskPresets = append(p.TaskPresets, p.TaskPresets[0]) },
		func(p *Profile) { p.TaskPresets[0].Icon = "unknown" },
		func(p *Profile) { p.TaskPresets = nil },
	} {
		p := base()
		invalid(&p)
		if !errors.Is(validateTaskPresets(&p), errcode.ErrBadRequest) {
			t.Fatal("invalid assistant tasks accepted")
		}
	}
}

func TestTaskIconCannotChangeExecutionInstructions(t *testing.T) {
	p := Profile{Instructions: "根据实际目标选择工具并验证交付成果"}
	preset := &TaskPreset{ID: "report", Name: "经营报告", Description: "分析本月销售数据", Icon: "chart-column"}
	req := CreateRequest{Content: "请完成报告"}
	before := assistantPromptContext(req, p, preset, "")
	preset.Icon = "code"
	if after := assistantPromptContext(req, p, preset, ""); after != before || strings.Contains(after.SystemInstructions(), "工作类型") || strings.Contains(after.SystemInstructions(), "软件开发任务：") {
		t.Fatal("changing a display icon changed execution behavior")
	}
	for _, value := range []any{ConfigResponse{Profiles: []Profile{p}}, p, req, Metadata{TaskPreset: preset}} {
		data, err := json.Marshal(value)
		if err != nil || strings.Contains(string(data), `"scenario"`) || strings.Contains(string(data), `"scenarios"`) {
			t.Fatalf("work type leaked into an API payload: %s %v", data, err)
		}
	}
}
