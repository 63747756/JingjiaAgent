package v1

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GoYoko/web"
	"github.com/alicebob/miniredis/v2"
	"github.com/chaitin/MonkeyCode/backend/consts"
	"github.com/chaitin/MonkeyCode/backend/db"
	"github.com/chaitin/MonkeyCode/backend/domain"
	"github.com/chaitin/MonkeyCode/backend/pkg/lifecycle"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type readyRepo struct {
	domain.HostRepo
	vm *db.VirtualMachine
}

func (r *readyRepo) GetVirtualMachine(context.Context, string) (*db.VirtualMachine, error) {
	return r.vm, nil
}

type readyRuntime struct {
	taskflow.Clienter
	req     taskflow.CreateTaskReq
	calls   int
	failure error
}

func (r *readyRuntime) StageTask(context.Context, taskflow.CreateTaskReq) (bool, error) {
	return true, nil
}
func (r *readyRuntime) PreparedTask(context.Context, string) (*taskflow.CreateTaskReq, error) {
	return &r.req, nil
}
func (r *readyRuntime) TaskManager() taskflow.TaskManager { return &readyManager{runtime: r} }

type readyManager struct {
	taskflow.TaskManager
	runtime *readyRuntime
}

func (m *readyManager) Create(context.Context, taskflow.CreateTaskReq) error {
	m.runtime.calls++
	return m.runtime.failure
}

func TestReadyAcknowledgesSQLAdmissionAndRetriesProcessingTasks(t *testing.T) {
	for _, status := range []consts.TaskStatus{consts.TaskStatusPending, consts.TaskStatusProcessing} {
		t.Run(string(status), func(t *testing.T) {
			id := uuid.New()
			runtime := &readyRuntime{req: taskflow.CreateTaskReq{ID: id, VMID: "ready-vm"}}
			mr := miniredis.RunT(t)
			rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
			defer rdb.Close()
			h := &InternalHostHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), taskflow: runtime, repo: &readyRepo{vm: &db.VirtualMachine{ID: "ready-vm", Edges: db.VirtualMachineEdges{Tasks: []*db.Task{{ID: id, UserID: uuid.New(), Status: status}}}}}, taskLifecycle: lifecycle.NewManager[uuid.UUID, consts.TaskStatus, lifecycle.TaskMetadata](rdb, lifecycle.WithTransitions[uuid.UUID, consts.TaskStatus, lifecycle.TaskMetadata](lifecycle.TaskTransitions()))}
			app := web.New()
			app.POST("/ready", web.BindHandler(h.VmReady))
			call := func() *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/ready", strings.NewReader(`{"id":"ready-vm"}`))
				req.Header.Set("Content-Type", "application/json")
				app.Echo().ServeHTTP(rec, req)
				return rec
			}
			runtime.failure = errors.New("SQL admission unavailable")
			first := call()
			if strings.Contains(first.Body.String(), `"code":0`) || runtime.calls != 1 {
				t.Fatal("admission failure was acknowledged or skipped", first.Body.String(), runtime.calls)
			}
			runtime.failure = nil
			mr.Close() // Redis notification failure must not undo SQL admission.
			second := call()
			if runtime.calls != 2 || !strings.Contains(second.Body.String(), `"code":0`) {
				t.Fatal("durable readiness could not recover independently of Redis")
			}
		})
	}
}
