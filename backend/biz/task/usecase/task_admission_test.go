package usecase

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	taskrepo "github.com/63747756/jingjiaagent/backend/biz/task/repo"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/pkg/entx"
	"github.com/63747756/jingjiaagent/backend/pkg/runtimeadapter"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
	"github.com/samber/do"
)

type taskAdmissionCapture struct {
	*runtimeadapter.Client
	after      func(context.Context, taskflow.SQLExecutor) error
	admittedVM taskflow.CreateVirtualMachineReq
}

func TestCreationReconciliationQuarantinesUncertaintyAndSerializesAdmission(t *testing.T) {
	for _, mode := range []string{"runtime-exists", "owner-mismatch", "admission-lock"} {
		t.Run(mode, func(t *testing.T) {
			f := newReviewFixture(t)
			ctx := context.Background()
			owner := f.db.User.Query().OnlyX(ctx)
			model := f.db.Model.Query().OnlyX(ctx)
			image := f.db.Image.Create().SetID(uuid.New()).SetUserID(owner.ID).SetName("reconcile-fixture").SaveX(ctx)
			i := do.New()
			do.ProvideValue(i, f.cfg)
			do.ProvideValue(i, f.db)
			do.ProvideValue(i, f.u.logger)
			r, err := taskrepo.NewTaskRepo(i)
			if err != nil {
				t.Fatal(err)
			}
			vmID := "agent_" + uuid.NewString()
			prepared, err := r.PrepareCreate(entx.WithTaskConcurrencyLimit(ctx, 3), &domain.User{ID: owner.ID}, domain.CreateTaskReq{Content: "creation reconciliation fixture", HostID: "node", ImageID: image.ID, ModelID: model.ID.String(), CliName: consts.CliNameOpencode, Resource: &domain.VMResource{Core: 1, Memory: 2 << 30}, Type: consts.TaskTypeDevelop}, "", vmID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.ExecContext(ctx, `UPDATE runtime_creation_attempts SET expires_at=now()-interval '1 hour'`); err != nil {
				t.Fatal(err)
			}
			if mode == "runtime-exists" {
				_, err = f.client.VirtualMachiner().Create(ctx, &taskflow.CreateVirtualMachineReq{ID: vmID, UserID: owner.ID.String(), HostID: "node", TaskID: prepared.ProjectTask.TaskID, Cores: "1", Memory: 2 << 30})
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "owner-mismatch" {
				if _, err = f.db.ExecContext(ctx, `UPDATE runtime_creation_attempts SET owner_id=$1`, uuid.New()); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "admission-lock" {
				tx, err := f.db.Tx(ctx)
				if err != nil {
					t.Fatal(err)
				}
				rows, err := tx.QueryContext(ctx, `SELECT id FROM virtualmachines WHERE id=$1 FOR UPDATE`, vmID)
				if err != nil {
					t.Fatal(err)
				}
				rows.Close()
				if err = f.client.ReconcileCreations(ctx); err != nil {
					t.Fatal(err)
				}
				if !f.db.VirtualMachine.Query().ExistX(ctx) || f.db.ModelApiKey.Query().CountX(ctx) != 1 {
					t.Fatal("reconciliation crossed admission lock")
				}
				tx.Rollback()
			}
			if err = f.client.ReconcileCreations(ctx); err != nil {
				t.Fatal(err)
			}
			rows, err := f.db.QueryContext(ctx, `SELECT state FROM runtime_creation_attempts WHERE vm_id=$1`, vmID)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var state string
			if !rows.Next() || rows.Scan(&state) != nil {
				t.Fatal("journal missing")
			}
			if mode == "admission-lock" {
				if state != "failed" || f.db.VirtualMachine.Query().ExistX(ctx) || f.db.ModelApiKey.Query().CountX(ctx) != 0 {
					t.Fatal("expired creation not cleaned after admission lock released")
				}
			} else if state != "uncertain" || !f.db.VirtualMachine.Query().ExistX(ctx) || f.db.ModelApiKey.Query().CountX(ctx) != 1 || f.db.Task.Query().OnlyX(ctx).Status != consts.TaskStatusPending {
				t.Fatal("uncertain or cross-owner state was compensated")
			}
		})
	}
}

func (c *taskAdmissionCapture) Host() taskflow.Hoster { return taskPreinsertHosterStub{} }
func (c *taskAdmissionCapture) AdmitTaskInTx(ctx context.Context, tx taskflow.SQLExecutor, vm taskflow.CreateVirtualMachineReq, req taskflow.CreateTaskReq) (*taskflow.VirtualMachine, bool, error) {
	c.admittedVM = vm
	result, handled, err := c.Client.AdmitTaskInTx(ctx, tx, vm, req)
	if err == nil && c.after != nil {
		err = c.after(ctx, tx)
	}
	return result, handled, err
}
func TestTaskBusinessAndRuntimeAdmissionCommitTogether(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "crash-before-commit"}[rollback], func(t *testing.T) {
			f := newReviewFixture(t)
			ctx := context.Background()
			owner := f.db.User.Query().OnlyX(ctx)
			model := f.db.Model.Query().OnlyX(ctx)
			image := f.db.Image.Create().SetID(uuid.New()).SetUserID(owner.ID).SetName("atomic-fixture").SaveX(ctx)
			i := do.New()
			do.ProvideValue(i, f.cfg)
			do.ProvideValue(i, f.db)
			do.ProvideValue(i, f.u.logger)
			r, err := taskrepo.NewTaskRepo(i)
			if err != nil {
				t.Fatal(err)
			}
			capture := &taskAdmissionCapture{Client: f.client.Client}
			injected := errors.New("fixture process ended before product commit")
			capture.after = func(ctx context.Context, tx taskflow.SQLExecutor) error {
				for _, table := range []string{"runtime_environments", "runtime_task_intents", "runtime_commands"} {
					if reviewSQLCount(t, tx, table) != 1 || reviewSQLCount(t, f.db, table) != 0 {
						t.Fatal("runtime became visible before business commit", table)
					}
				}
				if err := capture.Step(ctx); !errors.Is(err, sql.ErrNoRows) {
					t.Fatal("Worker saw uncommitted request", err)
				}
				vm := f.db.VirtualMachine.Query().OnlyX(ctx)
				if vm.EnvironmentID != "" {
					t.Fatal("product mapping became visible before commit")
				}
				if rollback {
					return injected
				}
				return nil
			}
			uc := &TaskUsecase{cfg: f.cfg, repo: r, logger: f.u.logger, taskflow: capture, redis: f.u.redis, taskLifecycle: f.u.taskLifecycle, vmLifecycle: f.u.vmLifecycle, taskActivityRefresher: noopTaskActivityRefresher{}, idleRefresher: noopVMIdleRefresher{}, dbClient: f.db}
			result, err := uc.Create(ctx, &domain.User{ID: owner.ID}, domain.CreateTaskReq{Content: "atomic task request", HostID: "node", ImageID: image.ID, ModelID: model.ID.String(), CliName: consts.CliNameOpencode, Resource: &domain.VMResource{Core: 1, Memory: 2 << 30}, Type: consts.TaskTypeDevelop})
			if capture.admittedVM.Cores != "1" || capture.admittedVM.Memory != 2<<30 {
				t.Fatal("selected product resources differ from runtime admission")
			}
			if rollback {
				if !errors.Is(err, injected) {
					t.Fatal("expected atomic rollback", err)
				}
				for _, table := range []string{"runtime_environments", "runtime_task_intents", "runtime_commands", "runtime_reservations"} {
					if reviewSQLCount(t, f.db, table) != 0 {
						t.Fatal("rolled back admission leaked", table)
					}
				}
				vm := f.db.VirtualMachine.Query().OnlyX(ctx)
				if vm.EnvironmentID != "" {
					t.Fatal("rollback changed product mapping")
				}
				if err = capture.ReconcileCreations(ctx); err != nil {
					t.Fatal("stale preparation reconciliation failed", err)
				}
				if f.db.Task.Query().OnlyX(ctx).Status != consts.TaskStatusError || f.db.VirtualMachine.Query().ExistX(ctx) || f.db.ModelApiKey.Query().CountX(ctx) != 0 {
					t.Fatal("expired incomplete task leaked VM, pending state or credential")
				}
				if err = capture.ReconcileCreations(ctx); err != nil {
					t.Fatal("repeated reconciliation failed", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result == nil {
				t.Fatal("missing business response")
			}
			vm := f.db.VirtualMachine.Query().OnlyX(ctx)
			if vm.EnvironmentID != vm.ID {
				t.Fatal("product mapping missing after commit")
			}
			req, err := capture.PreparedTask(ctx, result.Task.ID.String())
			if err != nil || req == nil || req.Text != "atomic task request" || req.VMID != vm.ID {
				t.Fatal("complete execution request missing after commit", err)
			}
			if _, err = f.db.ExecContext(ctx, `UPDATE runtime_creation_attempts SET expires_at=now()-interval '1 hour'`); err != nil {
				t.Fatal(err)
			}
			if err = capture.ReconcileCreations(ctx); err != nil {
				t.Fatal(err)
			}
			if f.db.Task.Query().OnlyX(ctx).Status != consts.TaskStatusPending || !f.db.VirtualMachine.Query().ExistX(ctx) || f.db.ModelApiKey.Query().CountX(ctx) != 1 {
				t.Fatal("reconciliation changed committed admission")
			}
		})
	}
}
