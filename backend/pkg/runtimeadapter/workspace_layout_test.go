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
			if len(selected) != 1 {
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
		}
	}
}
