package runtimeadapter

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestContinuationStatusIncludesFirstRoundWithoutPreparationPhase(t *testing.T) {
	for _, state := range []string{"pending", "running"} {
		t.Run(state, func(t *testing.T) {
			ledger, mock := recoveryLedger(t)
			owner, task := uuid.New(), uuid.New()
			environment, err := ledger.seal("env", mustJSON(taskflow.CreateVirtualMachineReq{ID: "env"}))
			if err != nil {
				t.Fatal(err)
			}
			mock.ExpectQuery("SELECT environment_id FROM runtime_task_intents").WithArgs(task.String()).WillReturnRows(sqlmock.NewRows([]string{"environment_id"}).AddRow("env"))
			mock.ExpectQuery("SELECT id,owner_id,node_id,project_id,sandbox_id,state,created_at,payload").WithArgs("env").WillReturnRows(sqlmock.NewRows([]string{"id", "owner_id", "node_id", "project_id", "sandbox_id", "state", "created_at", "payload"}).AddRow("env", owner.String(), "node", "project", "sandbox", "online", time.Now(), environment))
			input := taskflow.CreateTaskReq{ClientMessageID: uuid.NewString()}
			payload, err := ledger.seal("command", mustJSON(input))
			if err != nil {
				t.Fatal(err)
			}
			query := `SELECT id,state,preparation_phase,COALESCE(NULLIF(queue_reason,''),preparation_error),preparation_checkpoint_id,payload FROM runtime_commands WHERE task_id=$1 AND operation='task' AND state NOT IN ('complete','failed','canceled') ORDER BY turn LIMIT 10`
			mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(task).WillReturnRows(sqlmock.NewRows([]string{"id", "state", "phase", "error", "checkpoint_id", "payload"}).AddRow("command", state, "", "", nil, payload))
			items, err := (&Client{ledger: ledger}).Continuations(context.Background(), owner, task)
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 1 || items[0].State != state || items[0].Phase != "" || items[0].MessageID != input.ClientMessageID {
				t.Fatalf("first-round status lost its state or message identity: %+v", items)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
