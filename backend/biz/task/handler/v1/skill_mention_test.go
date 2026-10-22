package v1

import (
	"encoding/json"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
	"testing"
)

func TestSkillOnlyInputAndHistoryNormalization(t *testing.T) {
	id := uuid.New()
	raw, _ := json.Marshal(domain.TaskUserInputPayload{MentionedSkillID: &id, Content: []byte(""), Attachments: []domain.TaskAttachment{}})
	req := parseUserInputData(raw)
	if req.MentionedSkillID == nil || *req.MentionedSkillID != id || len(req.Content) != 0 {
		t.Fatal("skill-only request became JSON text")
	}
	echo, _ := json.Marshal(domain.TaskUserInputPayload{SkillMention: &taskflow.SkillMention{ID: id, Name: "report", Version: "v1"}, Content: []byte(""), Attachments: []domain.TaskAttachment{}})
	var normalized domain.TaskUserInputPayload
	if err := json.Unmarshal(normalizeUserInputData(echo), &normalized); err != nil || normalized.SkillMention == nil || len(normalized.Content) != 0 {
		t.Fatal("history normalization lost reference or showed execution prompt")
	}
}
