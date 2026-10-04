package entx

import (
	"context"
	"errors"
	"time"

	"github.com/chaitin/MonkeyCode/backend/consts"
	"github.com/chaitin/MonkeyCode/backend/db"
	"github.com/chaitin/MonkeyCode/backend/db/modelapikey"
	"github.com/chaitin/MonkeyCode/backend/db/task"
	"github.com/chaitin/MonkeyCode/backend/db/virtualmachine"
	"github.com/google/uuid"
)

// Only for a definitive rejection before the runtime environment committed.
// Never compensate an RPC timeout or an uncertain/accepted submission.
func RejectPreparedRuntimeCreation(ctx context.Context, client *db.Client, owner uuid.UUID, vmID string) error {
	return WithTx2(ctx, client, func(tx *db.Tx) error {
		vm, err := tx.VirtualMachine.Query().Where(virtualmachine.ID(vmID), virtualmachine.UserID(owner)).Only(ctx)
		if err != nil {
			return err
		}
		if vm.EnvironmentID != "" {
			return errors.New("cannot reject an admitted environment")
		}
		if _, err = tx.Task.Update().Where(task.UserID(owner), task.HasVmsWith(virtualmachine.ID(vmID)), task.Status(consts.TaskStatusPending)).SetStatus(consts.TaskStatusError).SetCompletedAt(time.Now()).Save(ctx); err != nil {
			return err
		}
		if _, err = tx.ModelApiKey.Delete().Where(modelapikey.UserID(owner), modelapikey.VirtualmachineID(vmID)).Exec(ctx); err != nil {
			return err
		}
		if err = tx.VirtualMachine.UpdateOneID(vmID).SetIsRecycled(true).Exec(ctx); err != nil {
			return err
		}
		_, err = tx.VirtualMachine.Delete().Where(virtualmachine.ID(vmID), virtualmachine.UserID(owner)).Exec(ctx)
		return err
	})
}
