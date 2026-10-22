package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/labstack/echo/v4"
)

type checkpointStore struct{ sandbox *domain.Sandbox }

func (s checkpointStore) GetSandbox(context.Context, string) (*domain.Sandbox, error) {
	return s.sandbox, nil
}

func TestJingjiaAgentRunningSnapshotAlwaysThawsBeforeResponse(t *testing.T) {
	for _, scenario := range []string{"success", "canceled", "pause-failed"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			os.MkdirAll(filepath.Join(root, ".jingjiaagent", "output"), 0o700)
			os.WriteFile(filepath.Join(root, ".jingjiaagent", "output", "report.txt"), []byte("report"), 0o600)
			sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-live", Driver: "docker", VMStatus: domain.VMStatusRunning, WorkspacePath: root}}
			app := echo.New()
			frozen, thaws := false, 0
			freeze := func(context.Context, *domain.Sandbox, domain.VMState) (func() error, error) {
				if scenario == "pause-failed" {
					return nil, errors.New("pause unavailable")
				}
				frozen = true
				return func() error {
					if frozen {
						frozen = false
						thaws++
					}
					return nil
				}, nil
			}
			registerJingjiaAgentWorkspace(app, checkpointStore{sandbox}, sandboxes.NewLifecycleLocks(), "secret", func(context.Context, *domain.Sandbox, domain.VMState) bool {
				t.Fatal("running save used the stopped-container path")
				return false
			}, freeze)
			request := httptest.NewRequest(http.MethodGet, "/jingjiaagent/workspace/export?sandbox_id=sandbox-live&checkpoint_id=check-live&mode=running", nil)
			request.Header.Set("Authorization", "Bearer secret")
			if scenario == "canceled" {
				ctx, cancel := context.WithCancel(request.Context())
				cancel()
				request = request.WithContext(ctx)
			}
			response := &thawedCheckpointResponse{ResponseRecorder: httptest.NewRecorder(), t: t, frozen: &frozen}
			app.ServeHTTP(response, request)
			want := 200
			if scenario == "canceled" {
				want = 422
			}
			if scenario == "pause-failed" {
				want = 503
			}
			if response.Code != want || frozen || sandbox.Summary.VMStatus != domain.VMStatusRunning {
				t.Fatalf("snapshot changed running lifecycle: status=%d frozen=%v", response.Code, frozen)
			}
			if scenario != "pause-failed" && thaws != 1 {
				t.Fatalf("failed to thaw after snapshot: %d", thaws)
			}
		})
	}
}

type thawedCheckpointResponse struct {
	*httptest.ResponseRecorder
	t      *testing.T
	frozen *bool
}

func (w *thawedCheckpointResponse) Write(body []byte) (int, error) {
	if *w.frozen {
		w.t.Fatal("archive network transfer started before thaw")
	}
	return w.ResponseRecorder.Write(body)
}
func (s checkpointStore) GetVMState(string) (domain.VMState, error) {
	return domain.VMState{Driver: "docker"}, nil
}
func TestJingjiaAgentWorkspaceExportAuthorizationAndState(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".jingjiaagent", "output"), 0o700)
	os.WriteFile(filepath.Join(root, ".jingjiaagent", "output", "report.txt"), []byte("report"), 0o600)
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", Driver: "docker", VMStatus: domain.VMStatusStopped, WorkspacePath: root}}
	app := echo.New()
	registerJingjiaAgentWorkspace(app, checkpointStore{sandbox}, sandboxes.NewLifecycleLocks(), "secret", func(context.Context, *domain.Sandbox, domain.VMState) bool { return true })
	for _, test := range []struct {
		query, auth, status string
		code                int
	}{
		{"sandbox_id=sandbox-1&checkpoint_id=check-1", "", "", 401},
		{"sandbox_id=sandbox-1&checkpoint_id=check-1", "Bearer secret", "", 200},
		{"sandbox_id=sandbox-1&checkpoint_id=check-1&host_path=/tmp", "Bearer secret", "", 400},
		{"sandbox_id=sandbox-1&checkpoint_id=check-1", "Bearer secret", domain.VMStatusRunning, 409},
	} {
		if test.status != "" {
			sandbox.Summary.VMStatus = test.status
		}
		request := httptest.NewRequest(http.MethodGet, "/jingjiaagent/workspace/export?"+test.query, nil)
		request.Header.Set("Authorization", test.auth)
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)
		if response.Code != test.code {
			body, _ := io.ReadAll(response.Result().Body)
			t.Fatalf("got %d want %d: %s", response.Code, test.code, body)
		}
	}
}
