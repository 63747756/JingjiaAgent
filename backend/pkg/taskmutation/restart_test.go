package taskmutation

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func restartTx(t *testing.T) (*sql.Tx, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	return tx, mock
}

func expectTask(mock sqlmock.Sqlmock, taskID uuid.UUID, owner uuid.UUID) {
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT user_id FROM tasks WHERE id=$1 FOR UPDATE`)).WithArgs(taskID.String()).WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(owner))
}

func expectSwitch(mock sqlmock.Sqlmock, taskID uuid.UUID, mutation *taskflow.RestartBusinessMutation, success any) {
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT success FROM task_model_switches WHERE id=$1 AND task_id=$2 AND user_id=$3 AND to_model_id=$4 FOR UPDATE`)).
		WithArgs(mutation.ModelSwitch.ID, taskID.String(), mutation.OwnerID, mutation.ModelSwitch.ModelID).
		WillReturnRows(sqlmock.NewRows([]string{"success"}).AddRow(success))
}

func modelMutation() (uuid.UUID, *taskflow.RestartBusinessMutation) {
	return uuid.New(), &taskflow.RestartBusinessMutation{OwnerID: uuid.New(), ModelSwitch: &taskflow.RestartModelSwitch{ID: uuid.New(), ModelID: uuid.New()}}
}

func TestApplyRestartModelSuccessIsAtomicAndDuplicateSafe(t *testing.T) {
	taskID, mutation := modelMutation()
	tx, mock := restartTx(t)
	expectTask(mock, taskID, mutation.OwnerID)
	expectSwitch(mock, taskID, mutation, nil)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE project_tasks SET model_id=$2 WHERE task_id=$1`)).WithArgs(taskID.String(), mutation.ModelSwitch.ModelID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE task_model_switches SET success=$2,message=$3,session_id=$4,updated_at=now() WHERE id=$1 AND success IS NULL`)).WithArgs(mutation.ModelSwitch.ID, true, "restarted", "session").WillReturnResult(sqlmock.NewResult(0, 1))
	response := taskflow.RestartTaskResp{Success: true, Message: "restarted", SessionID: "session"}
	if err := ApplyRestart(context.Background(), tx, taskID.String(), mutation, response); err != nil {
		t.Fatal(err)
	}
	// Even after another command has selected a different model, replaying this
	// completed audit must not update project_tasks again.
	expectTask(mock, taskID, mutation.OwnerID)
	expectSwitch(mock, taskID, mutation, true)
	if err := ApplyRestart(context.Background(), tx, taskID.String(), mutation, response); err != nil {
		t.Fatal(err)
	}
	mock.ExpectCommit()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyRestartFailureOnlyFinishesAudit(t *testing.T) {
	taskID, mutation := modelMutation()
	tx, mock := restartTx(t)
	expectTask(mock, taskID, mutation.OwnerID)
	expectSwitch(mock, taskID, mutation, nil)
	mock.ExpectExec("UPDATE task_model_switches SET success").WithArgs(mutation.ModelSwitch.ID, false, "canceled", "").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := ApplyRestart(context.Background(), tx, taskID.String(), mutation, taskflow.RestartTaskResp{Message: "canceled"}); err != nil {
		t.Fatal(err)
	}
	mock.ExpectCommit()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyRestartPersistenceFailureCannotBeAcknowledged(t *testing.T) {
	for _, rowCount := range []int64{0, 2} {
		t.Run(string(rune('0'+rowCount)), func(t *testing.T) {
			taskID, mutation := modelMutation()
			tx, mock := restartTx(t)
			expectTask(mock, taskID, mutation.OwnerID)
			expectSwitch(mock, taskID, mutation, nil)
			mock.ExpectExec("UPDATE project_tasks SET model_id").WithArgs(taskID.String(), mutation.ModelSwitch.ModelID).WillReturnResult(sqlmock.NewResult(0, rowCount))
			if err := ApplyRestart(context.Background(), tx, taskID.String(), mutation, taskflow.RestartTaskResp{Success: true}); err == nil {
				t.Fatal("missing/inconsistent task accepted")
			}
			mock.ExpectRollback()
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestApplyRestartAuditFailureRollsBackModel(t *testing.T) {
	taskID, mutation := modelMutation()
	tx, mock := restartTx(t)
	expectTask(mock, taskID, mutation.OwnerID)
	expectSwitch(mock, taskID, mutation, nil)
	mock.ExpectExec("UPDATE project_tasks SET model_id").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE task_model_switches SET success").WillReturnError(errors.New("fixture audit write failure"))
	if err := ApplyRestart(context.Background(), tx, taskID.String(), mutation, taskflow.RestartTaskResp{Success: true}); err == nil {
		t.Fatal("audit persistence failure accepted")
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyRestartResourceSelectionIncludesExplicitClear(t *testing.T) {
	for _, selection := range []*taskflow.RestartResourceSelection{{SkillIDs: []string{"skill"}, PluginIDs: []string{"plugin"}}, {}} {
		t.Run("selection", func(t *testing.T) {
			tx, mock := restartTx(t)
			taskID := uuid.New()
			mutation := &taskflow.RestartBusinessMutation{OwnerID: uuid.New(), ResourceSelection: selection}
			expectTask(mock, taskID, mutation.OwnerID)
			skills, plugins := `["skill"]`, `["plugin"]`
			if selection.SkillIDs == nil {
				skills, plugins = `[]`, `[]`
			}
			mock.ExpectExec(regexp.QuoteMeta(`UPDATE tasks SET skill_ids=$2::jsonb,plugin_ids=$3::jsonb WHERE id=$1 AND user_id=$4`)).WithArgs(taskID.String(), skills, plugins, mutation.OwnerID).WillReturnResult(sqlmock.NewResult(0, 1))
			if err := ApplyRestart(context.Background(), tx, taskID.String(), mutation, taskflow.RestartTaskResp{Success: true}); err != nil {
				t.Fatal(err)
			}
			mock.ExpectCommit()
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateRestartRejectsForeignOrConflictingMetadata(t *testing.T) {
	taskID, mutation := modelMutation()
	request := taskflow.RestartTaskReq{ID: taskID, RequestId: "request", LoadSession: true, BusinessMutation: mutation}
	if err := ValidateRestart(context.Background(), nil, taskID.String(), uuid.NewString(), request); err == nil {
		t.Fatal("foreign runtime owner accepted")
	}
	if err := ValidateRestart(context.Background(), nil, uuid.NewString(), mutation.OwnerID.String(), request); err == nil {
		t.Fatal("foreign task accepted")
	}
	tx, mock := restartTx(t)
	expectTask(mock, taskID, mutation.OwnerID)
	mock.ExpectQuery("SELECT EXISTS").WithArgs(mutation.ModelSwitch.ID, taskID.String(), mutation.OwnerID, mutation.ModelSwitch.ModelID, "request", true).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	if err := ValidateRestart(context.Background(), tx, taskID.String(), mutation.OwnerID.String(), request); err == nil {
		t.Fatal("conflicting audit accepted")
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyRestartRejectsOwnerOrTerminalConflict(t *testing.T) {
	for _, wrongOwner := range []bool{true, false} {
		t.Run("conflict", func(t *testing.T) {
			taskID, mutation := modelMutation()
			tx, mock := restartTx(t)
			owner := mutation.OwnerID
			if wrongOwner {
				owner = uuid.New()
			}
			expectTask(mock, taskID, owner)
			if !wrongOwner {
				expectSwitch(mock, taskID, mutation, false)
			}
			if err := ApplyRestart(context.Background(), tx, taskID.String(), mutation, taskflow.RestartTaskResp{Success: true}); err == nil {
				t.Fatal("conflicting mutation accepted")
			}
			mock.ExpectRollback()
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRestartMutationShapeFailsClosed(t *testing.T) {
	for _, mutation := range []*taskflow.RestartBusinessMutation{
		{},
		{OwnerID: uuid.New()},
		{OwnerID: uuid.New(), ModelSwitch: &taskflow.RestartModelSwitch{}},
		{OwnerID: uuid.New(), ModelSwitch: &taskflow.RestartModelSwitch{ID: uuid.New(), ModelID: uuid.New()}, ResourceSelection: &taskflow.RestartResourceSelection{}},
	} {
		if err := ApplyRestart(context.Background(), nil, uuid.NewString(), mutation, taskflow.RestartTaskResp{Success: true}); err == nil {
			t.Fatal("malformed business mutation accepted")
		}
	}
	if err := ApplyRestart(context.Background(), nil, uuid.NewString(), nil, taskflow.RestartTaskResp{Success: true}); err != nil {
		t.Fatalf("legacy plain restart rejected: %v", err)
	}
}
