package usecase

import (
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"testing"
)

func TestSkillExecutionPromptIsNotStoredAsTaskTitle(t *testing.T) {
	raw := ""
	r := domain.CreateTaskReq{Content: "compiled execution prompt", UserText: &raw, SkillMention: &taskflow.SkillMention{Name: "report"}}
	if taskStoredContent(r) != "report" {
		t.Fatal("compiled instruction exposed as task content")
	}
	raw = "分析这份表格"
	if taskStoredContent(r) != raw {
		t.Fatal("original user request lost")
	}
	r.UserText = nil
	if taskStoredContent(r) != r.Content {
		t.Fatal("legacy task content changed")
	}
}
