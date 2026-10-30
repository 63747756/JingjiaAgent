package runtimeadapter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

func TestWorkspaceRulesCoverCodingAndWorkWithoutChangingOriginalConfigs(t *testing.T) {
	for _, provider := range []taskflow.CodingAgent{taskflow.CodingAgentOpenCode, taskflow.CodingAgentCodex, taskflow.CodingAgentClaude} {
		for _, existing := range []bool{false, true} {
			text := "只处理本次用户要求"
			input := taskflow.CreateTaskReq{CodingAgent: provider, Text: text}
			if existing {
				input.Configs = []taskflow.ConfigFile{{Path: "${HOME}/.codingmatrix/project-tpl/.ai-ready/rules/team.md", Content: "TEAM RULE"}}
			}
			copy := workspaceTaskConfigs(input)
			if copy.Text != text {
				t.Fatal("workspace rules leaked into the user message")
			}
			if existing && input.Configs[0].Content != "TEAM RULE" {
				t.Fatal("saved assistant rules were mutated")
			}
			selected := nativeRuleFiles(copy, "/home/test")
			want := 1
			if existing {
				want++
				if copy.Configs[0].Content != "TEAM RULE" {
					t.Fatal("platform guidance was mixed into a team rule")
				}
			}
			if len(selected) != want {
				t.Fatalf("workspace rule selection missing or duplicated: %v", selected)
			}
			found := false
			for _, file := range copy.Configs {
				if strings.Contains(file.Content, workspaceNotice) {
					found = true
				}
				if provider == taskflow.CodingAgentOpenCode && strings.HasSuffix(file.Path, "opencode.json") {
					var cfg map[string]any
					if json.Unmarshal([]byte(file.Content), &cfg) != nil || len(cfg["instructions"].([]any)) != 1 {
						t.Fatal("OpenCode did not receive workspace instructions")
					}
				}
			}
			if !found {
				t.Fatal("native provider did not receive the workspace convention")
			}
			repeated := workspaceTaskConfigs(copy)
			if len(repeated.Configs) != len(copy.Configs) || len(nativeRuleFiles(repeated, "/home/test")) != want {
				t.Fatal("repeated preparation duplicated platform guidance")
			}
		}
	}
}

func TestWorkspaceRulePreservesOpenCodeConfigurationAndAvoidsDuplicateInstructions(t *testing.T) {
	for _, instructions := range [][]string{{"existing.md"}, {"${HOME}/.codingmatrix/project-tpl/.ai-ready/rules/*.md"}} {
		data, err := json.Marshal(map[string]any{"instructions": instructions, "plugin": []string{"installed-plugin"}, "provider": map[string]any{"company": map[string]any{"name": "retained"}}})
		if err != nil {
			t.Fatal(err)
		}
		input := taskflow.CreateTaskReq{CodingAgent: taskflow.CodingAgentOpenCode, Configs: []taskflow.ConfigFile{
			{Path: "~/.config/opencode/opencode.json", Content: string(data)},
			{Path: "${HOME}/.codingmatrix/project-tpl/.ai-ready/rules/jingjiaagent-workspace.md", Content: "previous platform convention"},
		}}
		updated := workspaceTaskConfigs(workspaceTaskConfigs(input))
		var cfg map[string]any
		if err := json.Unmarshal([]byte(updated.Configs[0].Content), &cfg); err != nil {
			t.Fatal(err)
		}
		want := 2
		if strings.HasSuffix(instructions[0], "*.md") {
			want = 1
		}
		if len(cfg["instructions"].([]any)) != want || cfg["instructions"].([]any)[0] != instructions[0] || cfg["plugin"].([]any)[0] != "installed-plugin" || cfg["provider"] == nil {
			t.Fatal("workspace preparation changed retained OpenCode settings or duplicated instructions")
		}
		if input.Configs[0].Content != string(data) || len(updated.Configs) != 2 || updated.Configs[1].Content != workspaceNotice {
			t.Fatal("workspace preparation mutated the input or retained obsolete platform guidance")
		}
	}
}
