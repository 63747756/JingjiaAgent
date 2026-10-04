package usecase

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/chaitin/MonkeyCode/backend/consts"
	"github.com/chaitin/MonkeyCode/backend/db"
	"github.com/chaitin/MonkeyCode/backend/domain"
	etypes "github.com/chaitin/MonkeyCode/backend/ent/types"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/google/uuid"
)

type preparationInfoRepo struct {
	domain.TaskRepo
	task *db.Task
	deny bool
}

func (r *preparationInfoRepo) Info(context.Context, *domain.User, uuid.UUID, bool) (*db.Task, error) {
	if r.deny {
		return nil, errors.New("access denied")
	}
	return r.task, nil
}
func (*preparationInfoRepo) Stat(context.Context, uuid.UUID) (*domain.TaskStats, error) {
	return nil, errors.New("stats unavailable")
}

type preparationInfoVM struct {
	taskflow.VirtualMachiner
	conditions []*taskflow.Condition
	err        error
	reads      int
}

func (v *preparationInfoVM) PreparationConditions(context.Context, string) ([]*taskflow.Condition, error) {
	v.reads++
	return v.conditions, v.err
}
func (*preparationInfoVM) IsOnline(context.Context, *taskflow.IsOnlineReq[string]) (*taskflow.IsOnlineResp, error) {
	return nil, errors.New("runtime temporarily unavailable")
}

func TestTaskInfoPreparationIsAuthorizedAndRetainsLegacyHistory(t *testing.T) {
	legacy := &etypes.Condition{Type: etypes.ConditionTypeImagePulled, Message: "legacy stage"}
	task := &db.Task{ID: uuid.New(), UserID: uuid.New(), Status: consts.TaskStatusPending,
		Edges: db.TaskEdges{Vms: []*db.VirtualMachine{{ID: "environment", CreatedAt: time.Now(),
			Conditions: &etypes.VirtualMachineCondition{Conditions: []*etypes.Condition{legacy}}}}}}
	repo := &preparationInfoRepo{task: task}
	vm := &preparationInfoVM{conditions: []*taskflow.Condition{{Type: "Scheduled", Reason: "RuntimePreparing", Status: taskflow.ConditionStatusInProgress}}}
	u := &TaskUsecase{repo: repo, taskflow: &taskPreinsertTaskflowStub{vm: vm}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	user := &domain.User{ID: task.UserID}
	repo.deny = true
	if _, _, err := u.Info(context.Background(), user, task.ID); err == nil || vm.reads != 0 {
		t.Fatal("preparation read preceded access authorization")
	}
	repo.deny = false
	detail, _, err := u.Info(context.Background(), user, task.ID)
	if err != nil || detail.VirtualMachine.Conditions[0].Reason != "RuntimePreparing" {
		t.Fatal("durable condition lost during runtime outage", err)
	}
	if task.Edges.Vms[0].Conditions.Conditions[0] != legacy {
		t.Fatal("read mutated persisted legacy history")
	}
	vm.conditions = nil
	detail, _, err = u.Info(context.Background(), user, task.ID)
	if err != nil || detail.VirtualMachine.Conditions[0].Message != legacy.Message {
		t.Fatal("legacy history replaced", err)
	}
	vm.err = errors.New("database unavailable")
	detail, _, err = u.Info(context.Background(), user, task.ID)
	if err != nil || detail.VirtualMachine.Conditions[0].Message != legacy.Message {
		t.Fatal("database outage became preparation failure", err)
	}
}
