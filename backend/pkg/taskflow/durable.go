package taskflow

import (
	"context"
	"database/sql"
)

// DurableCreator is an optional capability implemented by the runtime adapter.
// A nil PreparedTask denotes an existing legacy task, not a database error.
type DurableCreator interface {
	StageTask(context.Context, CreateTaskReq) (bool, error)
	PreparedTask(context.Context, string) (*CreateTaskReq, error)
}

// SQLExecutor lets a product repository stage admission on its own transaction.
// The caller owns commit/rollback; implementations must never commit it.
type SQLExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// TransactionalCreator makes the product task and its encrypted execution
// intent visible together. False denotes a legacy environment only.
type TransactionalCreator interface {
	StageTaskInTx(context.Context, SQLExecutor, CreateTaskReq) (bool, error)
}

// TransactionalTaskAdmission creates the environment, resource reservation and
// complete execution intent on the product transaction. False routes to legacy
// without making changes. It never sends runtime RPCs or commits the transaction.
type TransactionalTaskAdmission interface {
	AdmitTaskInTx(context.Context, SQLExecutor, CreateVirtualMachineReq, CreateTaskReq) (*VirtualMachine, bool, error)
}

// DurableStopper confirms every admitted Run has ended before business stop
// can recycle an environment or commit a final task status.
type DurableStopper interface {
	StopTaskAndWait(context.Context, string) (bool, error)
}

// PreparationReader exposes durable environment preparation without executing
// Guest commands. Product callers must authorize environment access first.
// Nil conditions leave the existing Taskflow condition history unchanged.
type PreparationReader interface {
	PreparationConditions(context.Context, string) ([]*Condition, error)
}
