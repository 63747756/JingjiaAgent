package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/63747756/jingjiaagent/backend/biz/mcphub/auth"
	"github.com/63747756/jingjiaagent/backend/biz/mcphub/repo"
	"github.com/google/uuid"
)

type builtinTestRegistry struct{}

func (builtinTestRegistry) ListEffectiveTools(context.Context, uuid.UUID) ([]repo.ToolSnapshot, error) {
	return nil, nil
}

type builtinTestAuth struct{ subject *auth.Subject }

func (a builtinTestAuth) Resolve(_ context.Context, token string) (*auth.Subject, error) {
	if token != "task-token" {
		return nil, auth.ErrUnauthorized
	}
	return a.subject, nil
}

type builtinTestRuntime struct {
	user, task uuid.UUID
	operation  string
	calls      int
}

func (r *builtinTestRuntime) BackgroundTerminal(_ context.Context, user, task uuid.UUID, op, _ string, _ json.RawMessage) (json.RawMessage, error) {
	r.user, r.task, r.operation = user, task, op
	r.calls++
	return json.RawMessage(`{"id":"bg_a"}`), nil
}
func (r *builtinTestRuntime) RequestPreview(_ context.Context, user, task uuid.UUID, _ int) (json.RawMessage, error) {
	r.user, r.task, r.operation = user, task, "preview"
	r.calls++
	return json.RawMessage(`{"access_url":"http://192.0.2.1:31000"}`), nil
}

func TestBuiltinToolsUseAuthenticatedTaskAndRequireNoExternalBilling(t *testing.T) {
	subject := &auth.Subject{UserID: uuid.New(), TaskID: uuid.New()}
	runtime := &builtinTestRuntime{}
	h := NewHandler(builtinTestRegistry{}, WithAuthResolver(builtinTestAuth{subject}), WithBuiltinRuntime(runtime))
	call := func(body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := call(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "task-token")
	for _, name := range []string{"background_terminal_create", "background_terminal_list", "background_terminal_output_path", "background_terminal_kill", "request_preview"} {
		if !strings.Contains(w.Body.String(), name) {
			t.Fatalf("missing builtin %s: %s", name, w.Body.String())
		}
	}
	w = call(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"background_terminal_list"}}`, "task-token")
	if w.Code != 200 || runtime.calls != 1 || runtime.user != subject.UserID || runtime.task != subject.TaskID || runtime.operation != "list" {
		t.Fatalf("wrong task scope: %+v %s", runtime, w.Body.String())
	}
	w = call(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"request_preview","arguments":{"port":8000,"task_id":"other"}}}`, "task-token")
	if runtime.calls != 1 || !strings.Contains(w.Body.String(), `"isError":true`) {
		t.Fatalf("caller-selected target accepted: %s", w.Body.String())
	}
	w = call(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"request_preview","arguments":{"port":8000}}}`, "bad-token")
	if w.Code != http.StatusUnauthorized || runtime.calls != 1 {
		t.Fatal("unauthenticated builtin reached runtime")
	}
	w = call(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"request_preview","arguments":{"port":8000}}}`, "task-token")
	if runtime.calls != 2 || runtime.operation != "preview" || !strings.Contains(w.Body.String(), "31000") {
		t.Fatalf("preview failed %s", w.Body.String())
	}
}
