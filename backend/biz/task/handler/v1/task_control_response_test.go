package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

func TestControlResponseDistinguishesPendingFromTerminalWithoutBreakingLegacyFields(t *testing.T) {
	for _, kind := range []string{"restart", "switch_model", "switch_agent_resources"} {
		for _, tc := range []struct {
			name    string
			result  any
			err     error
			status  string
			success bool
		}{
			{"pending", nil, &taskflow.RestartPendingError{Err: context.DeadlineExceeded}, "pending", false},
			{"wrapped-pending", nil, fmt.Errorf("observe: %w", &taskflow.RestartPendingError{Err: errors.New("database unavailable")}), "pending", false},
			{"rejected", nil, errors.New("permission denied"), "failed", false},
			{"terminal-failure", &taskflow.RestartTaskResp{Success: false, Message: "Agent rejected restart"}, nil, "failed", false},
			{"terminal-success", &domain.SwitchTaskModelResp{Success: true, Message: "done", SessionID: "session"}, nil, "succeeded", true},
			{"nil-result", nil, nil, "pending", false},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				data, err := marshalControlResponse(kind, "original-id", tc.result, tc.err)
				if err != nil {
					t.Fatal(err)
				}
				var response map[string]any
				if err = json.Unmarshal(data, &response); err != nil {
					t.Fatal(err)
				}
				if response["request_id"] != "original-id" || response["status"] != tc.status || response["success"] != tc.success {
					t.Fatalf("wrong wire result: %s", data)
				}
				if tc.err != nil && response["error"] != tc.err.Error() {
					t.Fatal("legacy error field changed")
				}
				if tc.success && (response["session_id"] != "session" || response["message"] != "done") {
					t.Fatal("terminal payload lost")
				}
			})
		}
	}
}
