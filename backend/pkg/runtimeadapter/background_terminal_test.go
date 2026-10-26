package runtimeadapter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestBackgroundArgumentsRejectScopeAndMalformedCommands(t *testing.T) {
	for _, tt := range []struct{ op, raw string }{
		{"create", `{"command":"sleep 10"}`},
		{"create", `{"command":"sleep 10","request_id":"../x"}`},
		{"create", `{"command":"sleep 10","request_id":"start-1","scope":"other-task"}`},
		{"create", `{"command":"\u0000","request_id":"start-1"}`},
		{"list", `{"terminal_id":"other"}`},
		{"kill", `{"terminal_id":"../escape"}`},
		{"output_path", `{"terminal_id":"valid","command":"evil"}`},
		{"create", `{"command":"sleep 1","request_id":"a"} {}`},
	} {
		if _, err := parseBackgroundArguments(tt.op, json.RawMessage(tt.raw)); err == nil {
			t.Errorf("accepted %s %s", tt.op, tt.raw)
		}
	}
	if _, err := parseBackgroundArguments("create", json.RawMessage(`{"command":"npm run dev","request_id":"web-1","cwd":"/workspace/app"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundTerminalCannotCrossTaskOwnerOrWakeEnvironment(t *testing.T) {
	c, req := workerFixture(t, &runTestServer{sandboxStopped: true})
	ctx := context.Background()
	if _, err := c.BackgroundTerminal(ctx, uuid.New(), req.ID, "list", "id", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("cross-owner access: %v", err)
	}
	env, err := c.ledger.EnvironmentForTask(ctx, req.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := uuid.Parse(env.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.BackgroundTerminal(ctx, owner, req.ID, "list", "id", json.RawMessage(`{}`)); err == nil {
		t.Fatal("stopped workspace accepted a background operation")
	}
}
