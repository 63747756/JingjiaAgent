package middleware

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGeneratedMemberCredentialsAreMaskedInAudit(t *testing.T) {
	for _, operation := range []string{"add_team_user_with_password", "add_team_admin", "reset_team_user_password"} {
		body := `{"code":0,"data":{"user":{"id":"member-id"},"password":"one-time-secret","passwords":[{"email":"a@example.invalid","password":"batch-secret"}]}}`
		req, resp, err := maskSensitiveData(operation, `{"email":"a@example.invalid","password":"ignored-input-secret"}`, body)
		if err != nil || strings.Contains(resp, "one-time-secret") || strings.Contains(resp, "batch-secret") || strings.Contains(req, "ignored-input-secret") {
			t.Fatal("audit retained generated credentials")
		}
		var result map[string]any
		if json.Unmarshal([]byte(resp), &result) != nil || result["code"] != float64(0) || !strings.Contains(resp, "member-id") || !strings.Contains(req, "a@example.invalid") {
			t.Fatal("credential masking lost audit context")
		}
	}
	_, resp, err := maskSensitiveData("add_team_admin", `{}`, `{"password":"truncated-secret`)
	if err != nil || strings.Contains(resp, "truncated-secret") {
		t.Fatal("invalid response retained credential fragment")
	}
}
