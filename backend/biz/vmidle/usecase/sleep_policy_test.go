package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/pkg/delayqueue"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/63747756/jingjiaagent/backend/pkg/vmrecycle"
	"github.com/google/uuid"
)

type sleepPolicyVMStub struct {
	taskflow.VirtualMachiner
	calls int
}

func (s *sleepPolicyVMStub) Hibernate(context.Context, *taskflow.HibernateVirtualMachineReq) error {
	s.calls++
	return nil
}

type sleepPolicyClientStub struct {
	taskflow.Clienter
	vm *sleepPolicyVMStub
}

func (s *sleepPolicyClientStub) VirtualMachiner() taskflow.VirtualMachiner { return s.vm }

func TestSleepJobUsesCurrentAdministratorPolicy(t *testing.T) {
	for _, scenario := range []string{"disabled", "not-due", "due", "recent-model-activity"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			last := time.Now().Add(-20 * time.Minute).Truncate(time.Second)
			task := &db.Task{ID: uuid.New(), LastActiveAt: last}
			vm := &db.VirtualMachine{ID: "policy-vm", UserID: uuid.New(), Edges: db.VirtualMachineEdges{Tasks: []*db.Task{task}}}
			r := newQueueTestVMIdleRefresher(newTestRedis(t), &refreshHostRepoStub{vm: vm})
			r.taskRepo = &recycleGuardTaskRepoStub{task: task}
			team := &db.Team{TaskConcurrencyLimit: 3, TaskTotalConcurrencyLimit: 5, TaskVMSleepEnabled: scenario != "disabled", TaskVMSleepSeconds: 600}
			if scenario == "not-due" {
				team.TaskVMSleepSeconds = 3600
			}
			if scenario == "recent-model-activity" {
				r.taskLogActivity = &taskLogActivityStub{latest: time.Now().Add(-time.Minute)}
			}
			r.teamPolicyRepo = &refreshTeamPolicyRepoStub{team: team}
			remote := &sleepPolicyVMStub{}
			r.taskflow = &sleepPolicyClientStub{vm: remote}
			err := r.handleSleepJob(ctx, &delayqueue.Job[*domain.VmIdleInfo]{Payload: &domain.VmIdleInfo{VmID: vm.ID}})
			switch scenario {
			case "not-due", "recent-model-activity":
				if !errors.Is(err, delayqueue.ErrJobRescheduled) || remote.calls != 0 {
					t.Fatalf("slept before the current deadline: %v calls=%d", err, remote.calls)
				}
				if scenario == "not-due" {
					assertVMIdleJobAt(t, r.sleepQueue.RedisDelayQueue, ctx, vmrecycle.SleepQueueKey, vm.ID, last.Add(time.Hour))
				}
			case "disabled":
				if err != nil || remote.calls != 0 {
					t.Fatalf("disabled sleep still executed: %v calls=%d", err, remote.calls)
				}
			case "due":
				if err != nil || remote.calls != 1 {
					t.Fatalf("policy deadline did not cause sleep: %v calls=%d", err, remote.calls)
				}
			}
		})
	}
}

func TestDisabledRecyclePolicyCancelsStaleQueuedJob(t *testing.T) {
	vm := &db.VirtualMachine{ID: "disabled-recycle-vm", UserID: uuid.New()}
	recycler := &idleRecyclerStub{}
	r := newRecycleGuardRefresher(t, vm, &recycleGuardTaskRepoStub{}, &taskLogActivityStub{}, recycler)
	r.teamPolicyRepo = &refreshTeamPolicyRepoStub{team: &db.Team{TaskConcurrencyLimit: 3, TaskTotalConcurrencyLimit: 5}}
	err := r.handleRecycleJob(context.Background(), &delayqueue.Job[*domain.VmIdleInfo]{Payload: &domain.VmIdleInfo{VmID: vm.ID, RecycleMethod: consts.VMRecycleMethodIdle}})
	if err != nil || recycler.calls != 0 {
		t.Fatalf("disabled recycle still executed: %v calls=%d", err, recycler.calls)
	}
}
