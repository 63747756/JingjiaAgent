package runtimeadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/DATA-DOG/go-sqlmock"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"github.com/google/uuid"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRecoveryHistoryAdvertisedOnlyAfterFileCommit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			l, m := recoveryLedger(t)
			e := Environment{ID: "env", OwnerID: "owner", NodeID: "node", SandboxID: "retained-sandbox", State: "online"}
			sealed, err := l.seal(e.ID, mustJSON(e.Request))
			if err != nil {
				t.Fatal(err)
			}
			server := &nativeModelFiles{files: map[string][]byte{}, pending: map[string][]byte{}, failCommit: fail}
			mux := http.NewServeMux()
			pattern, handler := rpc.NewExecServiceHandler(server)
			mux.Handle(pattern, handler)
			c := &Client{ledger: l, engines: map[string]*Engine{"node": testEngine(t, mux)}}
			task := taskflow.CreateTaskReq{ID: uuid.New(), Recovered: true, Text: "继续", RecoveryHistory: []string{"stale"}}
			cmd := Command{ID: uuid.NewString(), TaskID: task.ID.String(), Turn: 2}
			m.ExpectQuery("SELECT context_reset_id,context_reset_turn").WillReturnRows(sqlmock.NewRows([]string{"id", "turn"}).AddRow(nil, 0))
			m.ExpectQuery("SELECT id,payload FROM runtime_commands").WillReturnRows(sqlmock.NewRows([]string{"id", "payload"}))
			event := taskflow.TaskChunk{Event: "user-input", Data: mustJSON(map[string]any{"content": []byte("你好")})}
			m.ExpectQuery("SELECT seq,turn,chunk FROM runtime_events").WillReturnRows(sqlmock.NewRows([]string{"seq", "turn", "chunk"}).AddRow(1, 1, mustJSON(event)))
			m.ExpectQuery("SELECT id,owner_id,node_id,project_id,sandbox_id,state,created_at,payload").WillReturnRows(sqlmock.NewRows([]string{"id", "owner_id", "node_id", "project_id", "sandbox_id", "state", "created_at", "payload"}).AddRow(e.ID, e.OwnerID, e.NodeID, "project", e.SandboxID, e.State, time.Now(), sealed))
			if !fail {
				m.ExpectQuery("SELECT seq,turn,chunk FROM runtime_events").WillReturnRows(sqlmock.NewRows([]string{"seq", "turn", "chunk"}))
			}
			err = c.stageRecoveryHistory(context.Background(), e, cmd, &task)
			if fail {
				if err == nil || len(task.RecoveryHistory) != 0 || strings.Contains(executionPrompt(task), ".agent-history") {
					t.Fatal("failed history write advertised", err)
				}
			} else {
				if err != nil || len(task.RecoveryHistory) != 1 {
					t.Fatal(err, task.RecoveryHistory)
				}
				file := task.RecoveryHistory[0]
				if file != "/workspace/.jingjiaagent/work/.agent-history/"+cmd.ID+"-0000.json" || !json.Valid(server.files[file]) || !strings.Contains(executionPrompt(task), file) {
					t.Fatal("history prompt differs from committed file")
				}
			}
			if err := m.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
