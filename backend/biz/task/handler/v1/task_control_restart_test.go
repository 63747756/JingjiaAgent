package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"

	"github.com/chaitin/MonkeyCode/backend/consts"
	"github.com/chaitin/MonkeyCode/backend/domain"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/chaitin/MonkeyCode/backend/pkg/ws"
)

func TestControlRestartOnlyForwardsPublicFields(t *testing.T) {
	ownerID, taskID, modelID, switchID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, tc := range []struct {
		name        string
		loadSession bool
		injected    string
	}{
		{
			name:        "resource selection",
			loadSession: true,
			injected:    fmt.Sprintf(`"business_mutation":{"owner_id":%q,"resource_selection":{"skill_ids":["unvalidated-skill"],"plugin_ids":["unvalidated-plugin"]}}`, ownerID),
		},
		{
			name:     "model switch",
			injected: fmt.Sprintf(`"business_mutation":{"owner_id":%q,"model_switch":{"id":%q,"model_id":%q}}`, ownerID, switchID, modelID),
		},
		{
			name:        "execution config",
			loadSession: true,
			injected:    `"execution_config":{"llm":{"model":"unvalidated-model","api_key":"client-supplied-key"}}`,
		},
		{
			name:        "case insensitive internal fields",
			loadSession: true,
			injected:    fmt.Sprintf(`"BUSINESS_MUTATION":{"owner_id":%q,"resource_selection":{"skill_ids":[],"plugin_ids":[]}},"EXECUTION_CONFIG":{"llm":{"model":"unvalidated-model"}}`, ownerID),
		},
		{
			name:        "internal fields are never decoded",
			loadSession: true,
			injected:    `"business_mutation":"invalid-internal-type","execution_config":123,"resource_selection":{"skill_ids":["unvalidated-skill"]},"model_switch":{"model_id":"invalid-internal-id"}`,
		},
	} {
		for _, mode := range []string{"restart", "resume missing", "resume found"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				manager := &controlRestartTaskManager{requests: make(chan taskflow.RestartTaskReq, 1)}
				var taskManager taskflow.TaskManager = manager
				var resumer *controlRestartResumableTaskManager
				if mode != "restart" {
					resumer = &controlRestartResumableTaskManager{
						controlRestartTaskManager: manager,
						requests:                  make(chan taskflow.RestartTaskReq, 1),
						found:                     mode == "resume found",
					}
					taskManager = resumer
				}
				logger := slog.New(slog.NewTextHandler(io.Discard, nil))
				handler := &TaskHandler{
					logger:   logger,
					taskflow: &controlRestartTaskflow{manager: taskManager},
				}
				task := &domain.Task{ID: taskID, LogStore: consts.LogStoreLoki}
				user := &domain.User{ID: ownerID}
				serverErrors := make(chan error, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := ws.Accept(w, r)
					if err != nil {
						serverErrors <- err
						return
					}
					defer conn.Conn().CloseNow()
					// Exercise the actual public message dispatch and restart handler.
					_ = handler.controlReadMessages(r.Context(), conn, logger, user, task)
				}))
				t.Cleanup(server.Close)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.CloseNow() })
				// Even valid internal task and log-store overrides must be ignored.
				payload := fmt.Sprintf(`{"request_id":"public-restart","load_session":%t,"id":%q,"log_store":"client-selected-store",%s}`, tc.loadSession, uuid.New(), tc.injected)
				if err := wsjson.Write(ctx, conn, domain.TaskStream{
					Type: consts.TaskStreamTypeCall,
					Kind: "restart",
					Data: json.RawMessage(payload),
				}); err != nil {
					t.Fatal(err)
				}

				var response domain.TaskStream
				if err := wsjson.Read(ctx, conn, &response); err != nil {
					t.Fatalf("read restart response: %v", err)
				}
				if response.Type != consts.TaskStreamTypeCallResponse || response.Kind != "restart" {
					t.Fatalf("unexpected response envelope: %+v", response)
				}
				var result taskflow.RestartTaskResp
				if err := json.Unmarshal(response.Data, &result); err != nil || !result.Success || result.RequestId != "public-restart" {
					t.Fatalf("unexpected restart result: %s, %v", response.Data, err)
				}
				want := taskflow.RestartTaskReq{
					ID: taskID, RequestId: "public-restart", LoadSession: tc.loadSession, LogStore: string(task.LogStore),
				}
				if resumer != nil {
					assertControlRestartRequest(t, "ResumeRestart", resumer.requests, want)
				}
				if mode == "resume found" {
					select {
					case actual := <-manager.requests:
						t.Fatalf("replayed restart was admitted again: %+v", actual)
					default:
					}
				} else {
					assertControlRestartRequest(t, "Restart", manager.requests, want)
				}
				select {
				case err := <-serverErrors:
					t.Fatal(err)
				default:
				}
			})
		}
	}
}

func assertControlRestartRequest(t *testing.T, method string, requests <-chan taskflow.RestartTaskReq, want taskflow.RestartTaskReq) {
	t.Helper()
	select {
	case actual := <-requests:
		if !reflect.DeepEqual(actual, want) {
			t.Fatalf("TaskManager.%s received client-controlled internal fields: got %+v, want %+v", method, actual, want)
		}
	default:
		t.Fatalf("TaskManager.%s was not called", method)
	}
}

type controlRestartTaskflow struct {
	taskflow.Clienter
	manager taskflow.TaskManager
}

func (c *controlRestartTaskflow) TaskManager() taskflow.TaskManager { return c.manager }

type controlRestartTaskManager struct {
	taskflow.TaskManager
	requests chan taskflow.RestartTaskReq
}

func (m *controlRestartTaskManager) Restart(_ context.Context, req taskflow.RestartTaskReq) (*taskflow.RestartTaskResp, error) {
	m.requests <- req
	return &taskflow.RestartTaskResp{ID: req.ID, RequestId: req.RequestId, Success: true, SessionID: "server-session"}, nil
}

type controlRestartResumableTaskManager struct {
	*controlRestartTaskManager
	requests chan taskflow.RestartTaskReq
	found    bool
}

func (m *controlRestartResumableTaskManager) ResumeRestart(_ context.Context, req taskflow.RestartTaskReq) (*taskflow.RestartTaskResp, bool, error) {
	m.requests <- req
	if !m.found {
		return nil, false, nil
	}
	return &taskflow.RestartTaskResp{ID: req.ID, RequestId: req.RequestId, Success: true, SessionID: "server-session"}, true, nil
}
