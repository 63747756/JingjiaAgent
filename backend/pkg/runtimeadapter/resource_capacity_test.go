package runtimeadapter

import (
	"context"
	"errors"
	"testing"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

type rejectingResourceResolver struct {
	roundResolverFixture
	err    error
	checks int
}

func (r *rejectingResourceResolver) ValidateRoundResources(context.Context, *taskflow.CreateTaskReq) error {
	r.checks++
	return r.err
}

func TestCapacityRejectionDoesNotAcceptMessageAndAllowsCorrectedRetry(t *testing.T) {
	for _, work := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy-task", true: "work-task"}[work], func(t *testing.T) {
			var c *Client
			var req taskflow.CreateTaskReq
			if work {
				c, req, _ = continuationFixture(t)
			} else {
				l := testLedger(t)
				req = stageFixture(t, l)
				c = &Client{ledger: l, engines: map[string]*Engine{"node": {}}}
				if _, err := l.db.Exec(`UPDATE runtime_commands SET state='complete' WHERE task_id=$1`, req.ID); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			rejected := errors.New("resources exceed capacity")
			resolver := &rejectingResourceResolver{err: rejected}
			c.SetRoundSkillResolver(resolver)
			var before int
			if err := c.ledger.db.QueryRow(`SELECT count(*) FROM runtime_commands WHERE task_id=$1`, req.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			input := &taskflow.Task{ID: req.ID, Text: "preserve this draft", ClientMessageID: uuid.NewString()}
			if err := c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: input}); !errors.Is(err, rejected) {
				t.Fatalf("capacity rejection lost: %v", err)
			}
			var after int
			if err := c.ledger.db.QueryRow(`SELECT count(*) FROM runtime_commands WHERE task_id=$1`, req.ID).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Fatal("rejected message was enqueued")
			}
			resolver.err = nil
			if err := c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: input}); err != nil {
				t.Fatal(err)
			}
			if err := c.ledger.db.QueryRow(`SELECT count(*) FROM runtime_commands WHERE task_id=$1`, req.ID).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if after != before+1 {
				t.Fatal("corrected message not accepted exactly once")
			}
			if err := c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: input}); err != nil || resolver.checks != 2 {
				t.Fatalf("retry repeated resource validation: checks=%d %v", resolver.checks, err)
			}
		})
	}
}

func TestInitialAdmissionRejectsCombinedResourceCapacity(t *testing.T) {
	for _, atomic := range []bool{false, true} {
		t.Run(map[bool]string{false: "stage", true: "transactional-stage"}[atomic], func(t *testing.T) {
			l := testLedger(t)
			ctx := context.Background()
			id, vm := uuid.New(), "agent_"+uuid.NewString()
			if err := l.SaveEnvironment(ctx, Environment{ID: vm, OwnerID: uuid.NewString(), NodeID: "node", Request: taskflow.CreateVirtualMachineReq{ID: vm}}); err != nil {
				t.Fatal(err)
			}
			if _, err := l.db.Exec(`INSERT INTO tasks(id,status) VALUES($1,'pending')`, id); err != nil {
				t.Fatal(err)
			}
			rejected := errors.New("resources exceed capacity")
			c := &Client{ledger: l, engines: map[string]*Engine{"node": {}}, skillResolver: &rejectingResourceResolver{err: rejected}}
			req := taskflow.CreateTaskReq{ID: id, VMID: vm, Text: "new task"}
			var err error
			if atomic {
				tx, e := l.db.BeginTx(ctx, nil)
				if e != nil {
					t.Fatal(e)
				}
				_, err = c.StageTaskInTx(ctx, tx, req)
				tx.Rollback()
			} else {
				_, err = c.StageTask(ctx, req)
			}
			if !errors.Is(err, rejected) {
				t.Fatalf("invalid selection staged: %v", err)
			}
			var count int
			if err = l.db.QueryRow(`SELECT count(*) FROM runtime_commands WHERE task_id=$1`, id).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed admission persisted command: %d %v", count, err)
			}
		})
	}
}
