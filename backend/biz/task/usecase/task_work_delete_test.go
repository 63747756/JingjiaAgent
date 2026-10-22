package usecase

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/lifecycle"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type workDeleteRepo struct {
	domain.TaskRepo
	task         *db.Task
	softDeleted  bool
	deleteCalled int
}

func (r *workDeleteRepo) Info(context.Context, *domain.User, uuid.UUID, bool) (*db.Task, error) {
	return r.task, nil
}
func (r *workDeleteRepo) Delete(context.Context, *domain.User, uuid.UUID) error {
	r.deleteCalled++
	r.softDeleted = true
	return nil
}

type workDeleteVM struct {
	taskflow.VirtualMachiner
	err   error
	fail  map[string]error
	calls []taskflow.DeleteVirtualMachineReq
}

func (v *workDeleteVM) Delete(_ context.Context, req *taskflow.DeleteVirtualMachineReq) error {
	v.calls = append(v.calls, *req)
	if err, ok := v.fail[req.ID]; ok {
		return err
	}
	return v.err
}

type workDeleteFlow struct {
	taskflow.Clienter
	vm taskflow.VirtualMachiner
}

func (f *workDeleteFlow) VirtualMachiner() taskflow.VirtualMachiner { return f.vm }

type workDeleteHook struct {
	calls []string
	fn    func(string) error
}

func (h *workDeleteHook) Name() string  { return "test-work-cleanup" }
func (h *workDeleteHook) Priority() int { return 100 }
func (h *workDeleteHook) Async() bool   { return false }
func (h *workDeleteHook) OnStateChange(_ context.Context, id string, _, _ lifecycle.VMState, _ lifecycle.VMMetadata) error {
	h.calls = append(h.calls, id)
	if h.fn != nil {
		return h.fn(id)
	}
	return nil
}

func workDeleteFixture(t *testing.T, task *db.Task, vm *workDeleteVM) (*TaskUsecase, *workDeleteRepo, *workDeleteHook) {
	t.Helper()
	r := &workDeleteRepo{task: task}
	rdb := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { rdb.Close() })
	m := lifecycle.NewManager[string, lifecycle.VMState, lifecycle.VMMetadata](rdb,
		lifecycle.WithTransitions[string, lifecycle.VMState, lifecycle.VMMetadata](lifecycle.VMTransitions()))
	h := &workDeleteHook{}
	m.Register(h)
	return &TaskUsecase{repo: r, taskflow: &workDeleteFlow{vm: vm}, vmLifecycle: m,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, r, h
}

func TestWorkDeletePreservesTaskUntilRemoteRecycleConfirmed(t *testing.T) {
	for _, reason := range []string{
		"runtime environment still has active commands; stop before recycle",
		"成果尚未保存，已安排归档后再回收",
		"recorded runtime node is unavailable",
		"remote removal timed out",
		"permission denied: virtual_machine not found",
	} {
		t.Run(reason, func(t *testing.T) {
			uid, tid := uuid.New(), uuid.New()
			task := &db.Task{ID: tid, Kind: consts.TaskTypeWork, Edges: db.TaskEdges{Vms: []*db.VirtualMachine{{ID: "db-vm", EnvironmentID: "remote-env", HostID: "host"}}}}
			vm := &workDeleteVM{err: errors.New(reason)}
			u, repo, hook := workDeleteFixture(t, task, vm)
			err := u.Delete(context.Background(), &domain.User{ID: uid}, tid)
			if !errors.Is(err, errcode.ErrTaskDeletionPending) || repo.softDeleted || len(hook.calls) != 0 {
				t.Fatalf("unconfirmed recycle deleted task or queued lifecycle: %v deleted=%v hooks=%v", err, repo.softDeleted, hook.calls)
			}
			if len(vm.calls) != 1 || vm.calls[0].ID != "remote-env" || vm.calls[0].UserID != uid.String() || vm.calls[0].HostID != "host" {
				t.Fatalf("wrong authorized remote target: %#v", vm.calls)
			}
		})
	}
}

func TestWorkDeleteRequiresConfirmationEvenWithLocalRecycledFlag(t *testing.T) {
	task := &db.Task{ID: uuid.New(), Kind: consts.TaskTypeWork, Edges: db.TaskEdges{Vms: []*db.VirtualMachine{{ID: "vm", HostID: "host", IsRecycled: true}}}}
	u, repo, _ := workDeleteFixture(t, task, &workDeleteVM{err: errors.New("remote removal unknown")})
	if err := u.Delete(context.Background(), &domain.User{ID: uuid.New()}, task.ID); !errors.Is(err, errcode.ErrTaskDeletionPending) || repo.softDeleted {
		t.Fatalf("local flag bypassed formal recycle: %v", err)
	}
}

func TestWorkDeleteCleansEveryEnvironmentOnlyAfterRemoteConfirmation(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "removed", true: "already removed"}[missing], func(t *testing.T) {
			task := &db.Task{ID: uuid.New(), Kind: consts.TaskTypeWork, Edges: db.TaskEdges{Vms: []*db.VirtualMachine{{ID: "vm-1", HostID: "host"}, {ID: "vm-2", EnvironmentID: "env-2", HostID: "host"}}}}
			vm := &workDeleteVM{}
			if missing {
				vm.err = errors.New("virtual_machine not found")
			}
			u, repo, hook := workDeleteFixture(t, task, vm)
			hook.fn = func(id string) error {
				if len(vm.calls) != 2 || repo.softDeleted {
					t.Fatal("cleanup started before all remote environments were confirmed")
				}
				return nil
			}
			if err := u.Delete(context.Background(), &domain.User{ID: uuid.New()}, task.ID); err != nil || !repo.softDeleted || len(hook.calls) != 2 {
				t.Fatalf("confirmed deletion: %v deleted=%v hooks=%v", err, repo.softDeleted, hook.calls)
			}
		})
	}
}

func TestWorkDeleteBlocksOnAnyUnconfirmedEnvironment(t *testing.T) {
	task := &db.Task{ID: uuid.New(), Kind: consts.TaskTypeWork, Edges: db.TaskEdges{Vms: []*db.VirtualMachine{{ID: "vm-1", HostID: "host"}, {ID: "vm-2", HostID: "host"}}}}
	vm := &workDeleteVM{fail: map[string]error{"vm-2": errors.New("active commands")}}
	u, repo, hook := workDeleteFixture(t, task, vm)
	if err := u.Delete(context.Background(), &domain.User{ID: uuid.New()}, task.ID); !errors.Is(err, errcode.ErrTaskDeletionPending) || repo.softDeleted || len(hook.calls) != 0 {
		t.Fatalf("second unconfirmed environment soft-deleted task: %v", err)
	}
}

func TestDevelopDeleteKeepsExistingLifecycleBehavior(t *testing.T) {
	task := &db.Task{ID: uuid.New(), Kind: consts.TaskTypeDevelop, Edges: db.TaskEdges{Vms: []*db.VirtualMachine{{ID: "vm-1", HostID: "host"}, {ID: "vm-2", HostID: "host"}}}}
	vm := &workDeleteVM{err: errors.New("unused direct deletion")}
	u, repo, hook := workDeleteFixture(t, task, vm)
	hook.fn = func(string) error { return errors.New("existing asynchronous lifecycle retry") }
	if err := u.Delete(context.Background(), &domain.User{ID: uuid.New()}, task.ID); err != nil || !repo.softDeleted || len(vm.calls) != 0 || len(hook.calls) != 1 {
		t.Fatalf("legacy behavior changed: %v direct=%v hooks=%v", err, vm.calls, hook.calls)
	}
}
