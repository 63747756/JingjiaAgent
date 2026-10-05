package runtimeadapter

import (
	"reflect"
	"testing"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

func TestNativeRuleSelectionDoesNotGlobOldRules(t *testing.T) {
	request := taskflow.CreateTaskReq{Configs: []taskflow.ConfigFile{
		{Path: "${HOME}\\.codingmatrix\\project-tpl\\.ai-ready\\rules\\中文.md"},
		{Path: "~/.codingmatrix/project-tpl/.ai-ready/rules/exact.md"},
		{Path: "~/.claude/settings.json"},
		{Path: "~/.codingmatrix/project-tpl/.ai-ready/rules/../other.md"},
	}}
	actual := nativeRuleFiles(request, "/data/home")
	want := []string{"/data/home/.codingmatrix/project-tpl/.ai-ready/rules/中文.md", "/data/home/.codingmatrix/project-tpl/.ai-ready/rules/exact.md"}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("unexpected selected rules: %v", actual)
	}
	if len(nativeRuleFiles(taskflow.CreateTaskReq{}, "/data/home")) != 0 {
		t.Fatal("empty configuration selected old files")
	}
}
