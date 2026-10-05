package runtimeadapter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

func TestGuestConfigPathsPreserveCredentialsAndClearPluginSelections(t *testing.T) {
	input := `{"instructions":["${HOME}/.codingmatrix/rules/*.md"],"skills":{"paths":["${HOME}\\.codingmatrix\\skills"]},"plugin":["file://${HOME}/old.ts"],"provider":{"private":{"options":{"apiKey":"literal-${HOME}-key"}}}}`
	keep, err := renderOpenCodePaths(input, "/root", nil)
	if err != nil || !strings.Contains(keep, "file:///root/old.ts") || !strings.Contains(keep, "literal-${HOME}-key") || !strings.Contains(keep, "/root/.codingmatrix/skills") {
		t.Fatalf("declared path expansion damaged configuration: %v", err)
	}
	cleared, err := renderOpenCodePaths(input, "/root", &taskflow.AgentResources{})
	var config map[string]any
	if err != nil || json.Unmarshal([]byte(cleared), &config) != nil || len(config["plugin"].([]any)) != 0 {
		t.Fatal("empty resource selection retained a previous plugin")
	}
	if value := guestConfigPath(`${HOME}\.codingmatrix\rules\中文.md`, "/root"); value != "/root/.codingmatrix/rules/中文.md" {
		t.Fatal("Windows-produced ConfigFile path did not address the Guest home")
	}
	if _, err = renderOpenCodePaths(`null`, "/root", &taskflow.AgentResources{}); err == nil {
		t.Fatal("null OpenCode configuration was accepted")
	}
}
