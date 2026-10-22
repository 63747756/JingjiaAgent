package runtimeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"testing"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestRecoveryHistoryCollectsVerifiedLocationsOnlyWhenSessionIsLost(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success-and-retry", true: "download-failed"}[fail], func(t *testing.T) {
			server := &workInputStagingServer{requests: make(chan workInputStageRequest, 8), fail: fail}
			c, mock := workInputTestClient(t, server)
			c.objectStorage.Enabled = false
			tid := uuid.New()
			cmd := Command{ID: uuid.NewString(), TaskID: tid.String(), Turn: 4}
			env := Environment{ID: "env", OwnerID: uuid.NewString(), NodeID: "node", SandboxID: "sandbox"}
			task := taskflow.CreateTaskReq{ID: tid, Text: "继续分析", RecoveryContext: "历史对话已经重建"}
			// A business history projection alone must not replay any attachments.
			if err := c.stageRecoveryHistory(context.Background(), env, cmd, &task); err != nil || len(server.requests) != 0 {
				t.Fatal("live native session received a recovery inventory", err)
			}
			task.Recovered = true
			initial := taskflow.AttachmentLocation{Filename: "初始文件.txt", Path: "/workspace/input/fixed/1-初始文件.txt"}
			task.RecoveryInputs = []taskflow.AttachmentLocation{initial}
			first, later, empty := uuid.NewString(), uuid.NewString(), uuid.NewString()
			encode := func(id, filename string) []byte {
				previous := taskflow.CreateTaskReq{ID: tid}
				if filename != "" {
					previous.Attachments = []taskflow.Attachment{{Filename: filename, URL: "https://files.invalid/data?credential=private"}}
				}
				sealed, err := c.ledger.seal(id, mustJSON(previous))
				if err != nil {
					t.Fatal(err)
				}
				return sealed
			}
			for attempt := 0; attempt < 2; attempt++ {
				mock.ExpectQuery("SELECT context_reset_id,context_reset_turn").WithArgs(cmd.TaskID).
					WillReturnRows(sqlmock.NewRows([]string{"id", "turn"}).AddRow(nil, 0))
				mock.ExpectQuery("SELECT id,payload FROM runtime_commands").WithArgs(cmd.TaskID, cmd.Turn).
					WillReturnRows(sqlmock.NewRows([]string{"id", "payload"}).AddRow(first, encode(first, "同名.txt")).AddRow(empty, encode(empty, "")).AddRow(later, encode(later, "同名.txt")))
				if !fail {
					mock.ExpectQuery("SELECT seq,turn,chunk FROM runtime_events").WithArgs(cmd.TaskID, cmd.Turn, int64(0), 0).
						WillReturnRows(sqlmock.NewRows([]string{"seq", "turn", "chunk"}))
				}
				err := c.stageRecoveryHistory(context.Background(), env, cmd, &task)
				if fail {
					if err == nil || len(task.RecoveryInputs) != 1 || task.RecoveryInputs[0] != initial {
						t.Fatal("failed download advertised unverified historical files", err)
					}
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(task.RecoveryInputs) != 3 {
					t.Fatalf("recovery inventory missing or duplicated files: %+v", task.RecoveryInputs)
				}
				prompt := executionPrompt(task)
				for _, filename := range []string{initial.Path, path.Join("/workspace/.jingjiaagent/attachments", tid.String(), first, "1-同名.txt"), path.Join("/workspace/.jingjiaagent/attachments", tid.String(), later, "1-同名.txt")} {
					if strings.Count(prompt, filename) != 1 {
						t.Fatalf("missing or duplicated verified location %q: %s", filename, prompt)
					}
				}
				if strings.Count(prompt, "已恢复的可用附件") != 1 || strings.Contains(prompt, "credential=") || strings.Contains(prompt, empty) {
					t.Fatal("recovery included empty batches, credentials or duplicate headers")
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRecoveryWithoutAttachmentsOmitsInventory(t *testing.T) {
	task := taskflow.CreateTaskReq{Recovered: true, RecoveryContext: "重新建立的对话背景", Text: "你好"}
	prompt := executionPrompt(task)
	if strings.Contains(prompt, "已恢复的可用附件") || strings.Contains(prompt, "/workspace/input") || !strings.Contains(prompt, task.RecoveryContext) || !strings.HasSuffix(prompt, task.Text) {
		t.Fatalf("empty recovery invented attachment paths or lost context: %s", prompt)
	}
}

func TestRecoveredLocationsPersistForRetryAndClearForNextInput(t *testing.T) {
	c, request, _ := continuationFixture(t)
	cmd := acceptedContinuation(t, c, request, "恢复后继续")
	var task taskflow.CreateTaskReq
	if err := json.Unmarshal(cmd.Payload, &task); err != nil {
		t.Fatal(err)
	}
	task.Recovered, task.RecoveryContext = true, "仅供本轮恢复的背景"
	task.RecoveryHistory = []string{"/workspace/.jingjiaagent/work/.agent-history/current-0000.json"}
	task.RecoveryInputs = []taskflow.AttachmentLocation{{Filename: "附件.txt", Path: "/workspace/input/task/round/1-附件.txt"}}
	for attempt := 0; attempt < 2; attempt++ {
		if err := c.persistPreparedRound(context.Background(), &cmd, task); err != nil {
			t.Fatal(err)
		}
		var sealed []byte
		if err := c.ledger.db.QueryRow(`SELECT payload FROM runtime_commands WHERE id=$1`, cmd.ID).Scan(&sealed); err != nil {
			t.Fatal(err)
		}
		raw, err := c.ledger.open(cmd.ID, sealed)
		if err != nil {
			t.Fatal(err)
		}
		var replay taskflow.CreateTaskReq
		if err = json.Unmarshal(raw, &replay); err != nil || executionPrompt(replay) != executionPrompt(task) || replay.InputText() != "恢复后继续" {
			t.Fatal("retry did not preserve the accepted recovery prompt and raw input", err)
		}
	}
	// A changed snapshot cannot cross the admission fence.
	if _, err := c.ledger.db.Exec(`UPDATE runtime_commands SET submission_started=true WHERE id=$1`, cmd.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.persistPreparedRound(context.Background(), &cmd, task); !errors.Is(err, errLeaseLost) {
		t.Fatal("admitted round accepted a replacement recovery inventory", err)
	}
	if _, err := c.ledger.db.Exec(`UPDATE runtime_commands SET state='complete',lease_token=NULL,lease_until=NULL WHERE id=$1`, cmd.ID); err != nil {
		t.Fatal(err)
	}
	// Even an old task intent containing a recovery snapshot must not carry it
	// forward when accepting a new user message.
	sealed, err := c.ledger.seal(request.ID.String(), mustJSON(task))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ledger.db.Exec(`UPDATE runtime_task_intents SET payload=$2 WHERE task_id=$1`, request.ID, sealed); err != nil {
		t.Fatal(err)
	}
	next := acceptedContinuation(t, c, request, "正常的下一轮")
	var normal taskflow.CreateTaskReq
	if err = json.Unmarshal(next.Payload, &normal); err != nil {
		t.Fatal(err)
	}
	if normal.Recovered || normal.RecoveryContext != "" || len(normal.RecoveryInputs) != 0 || len(normal.RecoveryHistory) != 0 || executionPrompt(normal) != "正常的下一轮" {
		t.Fatal("next input inherited the prior recovery inventory")
	}
}

func TestRecoveryPromptOnlyNamesSuccessfullyStagedHistory(t *testing.T) {
	task := taskflow.CreateTaskReq{Recovered: true, Text: "继续修改"}
	if strings.Contains(executionPrompt(task), ".agent-history") {
		t.Fatal("invented history without files")
	}
	task.RecoveryHistory = []string{"/workspace/.jingjiaagent/work/.agent-history/round-0000.json"}
	if !strings.Contains(executionPrompt(task), task.RecoveryHistory[0]) {
		t.Fatal("missing actual history path")
	}
	task.Recovered = false
	if strings.Contains(executionPrompt(task), ".agent-history") {
		t.Fatal("ordinary continuation repeated history")
	}
}
