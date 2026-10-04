package runtimeadapter

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func (c *Client) AdmitTaskInTx(ctx context.Context, tx taskflow.SQLExecutor, vm taskflow.CreateVirtualMachineReq, task taskflow.CreateTaskReq) (*taskflow.VirtualMachine, bool, error) {
	if tx == nil {
		return nil, true, errors.New("product transaction required")
	}
	var known bool
	if err := scanSQLRow(ctx, tx, `SELECT EXISTS(SELECT 1 FROM runtime_environments WHERE id=$1)`, []any{vm.ID}, &known); err != nil {
		return nil, true, err
	}
	if c.backend == "taskflow" && !known {
		return nil, false, nil
	}
	owner, err := uuid.Parse(vm.UserID)
	if err != nil || owner == uuid.Nil || vm.ID == "" || task.VMID != vm.ID || task.ID == uuid.Nil || vm.TaskID != task.ID {
		return nil, true, errors.New("invalid atomic task admission")
	}
	if c.engines[vm.HostID] == nil {
		return nil, true, errors.New("selected host is not a configured runtime node")
	}
	if c.registry != nil && !c.nodeIsVerified(vm.HostID) {
		return nil, true, errors.New("runtime node is not verified by this process")
	}
	if known {
		var savedOwner, node string
		if err = scanSQLRow(ctx, tx, `SELECT owner_id,node_id FROM runtime_environments WHERE id=$1 FOR UPDATE`, []any{vm.ID}, &savedOwner, &node); err != nil {
			return nil, true, err
		}
		if savedOwner != vm.UserID || node != vm.HostID {
			return nil, true, errors.New("runtime environment submission payload conflict")
		}
	}
	e := Environment{ID: vm.ID, OwnerID: vm.UserID, NodeID: vm.HostID, State: "pending", CreatedAt: time.Now(), Request: vm}
	if err = c.ledger.saveEnvironmentTx(ctx, tx, e, false); err != nil {
		return nil, true, err
	}
	staged, err := c.StageTaskInTx(ctx, tx, task)
	if err != nil {
		return nil, true, err
	}
	if !staged {
		return nil, true, sql.ErrNoRows
	}
	return projectVM(e), true, nil
}
