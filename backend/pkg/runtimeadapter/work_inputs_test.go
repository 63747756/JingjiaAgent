package runtimeadapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/DATA-DOG/go-sqlmock"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"github.com/google/uuid"
)

type workInputStageRequest struct {
	TaskID      string                `json:"task_id"`
	CommandID   string                `json:"command_id"`
	WorkMode    bool                  `json:"work_mode"`
	Attachments []taskflow.Attachment `json:"attachments"`
}

type workInputStagingServer struct {
	rpc.UnimplementedExecServiceHandler
	requests   chan workInputStageRequest
	fail       bool
	badReceipt string
}

func (s *workInputStagingServer) Exec(_ context.Context, r *connect.Request[v2.ExecRequest]) (*connect.Response[v2.ExecResponse], error) {
	parts := strings.Split(r.Msg.Command.Args[1], "'")
	raw, err := base64.StdEncoding.DecodeString(parts[len(parts)-2])
	if err != nil {
		return nil, err
	}
	var request workInputStageRequest
	if err = json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	s.requests <- request
	if s.fail {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("attachment download failed"))
	}
	root := path.Join("/workspace/.jingjiaagent/attachments", request.TaskID, request.CommandID)
	files := []taskflow.AttachmentLocation{}
	for index, attachment := range request.Attachments {
		files = append(files, taskflow.AttachmentLocation{Filename: attachment.Filename, Path: path.Join(root, fmt.Sprintf("%d-%s", index+1, attachment.Filename))})
	}
	if len(files) > 0 && s.badReceipt != "" {
		switch s.badReceipt {
		case "missing":
			files = nil
		case "name":
			files[0].Filename = "foreign.txt"
		case "path":
			files[0].Path = "/workspace/input/another-task/foreign.txt"
		}
	}
	return connect.NewResponse(&v2.ExecResponse{Result: &v2.ExecResult{Stdout: string(mustJSON(map[string]any{"data": map[string]any{"installed": true, "files": files}}))}}), nil
}

func workInputTestClient(t *testing.T, server *workInputStagingServer) (*Client, sqlmock.Sqlmock) {
	t.Helper()
	ledger, mock := recoveryLedger(t)
	ledger.workEnabled = true
	mux := http.NewServeMux()
	pattern, handler := rpc.NewExecServiceHandler(server)
	mux.Handle(pattern, handler)
	c := resourceRefreshClient()
	c.ledger = ledger
	c.engines = map[string]*Engine{"node": testEngine(t, mux)}
	return c, mock
}

func TestWorkInputsPreserveRawMessageAndSystemContext(t *testing.T) {
	for _, submitted := range []bool{false, true} {
		for _, empty := range []string{"", " \t\n"} {
			for _, preset := range []string{"null", `{}`, `{"name":" ","description":"\n"}`} {
				t.Run(preset+empty+map[bool]string{false: "/new", true: "/replay"}[submitted], func(t *testing.T) {
					server := &workInputStagingServer{requests: make(chan workInputStageRequest, 1)}
					c, mock := workInputTestClient(t, server)
					text := "你好"
					task := taskflow.CreateTaskReq{ID: uuid.New(), Text: text, UserText: &text, SystemPrompt: "助手说明、项目规则及输出约定"}
					cmd := Command{ID: uuid.NewString(), TaskID: task.ID.String(), Submitted: submitted}
					mock.ExpectQuery("SELECT resource_manifest FROM work_tasks").WithArgs(cmd.TaskID).
						WillReturnRows(sqlmock.NewRows([]string{"resources"}).AddRow([]byte(`[]`)))
					if err := c.stageWorkInputs(context.Background(), Environment{ID: "env", NodeID: "node", SandboxID: "sandbox"}, cmd, &task); err != nil {
						t.Fatal(err)
					}
					for _, absent := range []string{"工作背景", "/workspace/input", "用户资料", "用户本轮附件", "读取资料", "initial_inputs", "task_preset", cmd.ID, task.ID.String()} {
						if strings.Contains(task.Text, absent) {
							t.Fatalf("empty inputs advertised %q: %s", absent, task.Text)
						}
					}
					if task.InputText() != text || task.Text != text || task.SystemPrompt != "助手说明、项目规则及输出约定" {
						t.Fatalf("user text or system context changed: %+v", task)
					}
					if !submitted {
						request := <-server.requests
						if !request.WorkMode || len(request.Attachments) != 0 {
							t.Fatal("empty work staging contract changed")
						}
					} else if len(server.requests) != 0 {
						t.Fatal("submitted command staged inputs again")
					}
					if err := mock.ExpectationsWereMet(); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestWorkInputsDoNotRepeatInitialFilesAndPreserveContext(t *testing.T) {
	for _, scenario := range []string{"new", "replay", "continuation", "recovery", "recovery-replay", "failed-download"} {
		t.Run(scenario, func(t *testing.T) {
			server := &workInputStagingServer{requests: make(chan workInputStageRequest, 1), fail: scenario == "failed-download"}
			c, mock := workInputTestClient(t, server)
			text := "分析这些资料"
			current := taskflow.Attachment{Filename: "本轮新增.txt", URL: "https://files.invalid/private?token=secret"}
			task := taskflow.CreateTaskReq{ID: uuid.New(), Text: text, UserText: &text, Attachments: []taskflow.Attachment{current}, Recovered: strings.HasPrefix(scenario, "recovery")}
			cmd := Command{ID: uuid.NewString(), TaskID: task.ID.String(), Turn: 1, Submitted: strings.HasSuffix(scenario, "replay")}
			if scenario == "continuation" || task.Recovered {
				cmd.Turn = 3
			}
			env := Environment{ID: "env", NodeID: "node", SandboxID: "sandbox"}
			pins := []workInputPin{{VersionID: uuid.New(), Filename: "销售 数据.xlsx", SHA256: strings.Repeat("a", 64)}, {VersionID: uuid.New(), Filename: "销售 数据.xlsx", SHA256: strings.Repeat("b", 64)}}
			root := path.Join("/workspace/.jingjiaagent/attachments", task.ID.String(), uuid.NewSHA1(task.ID, []byte(env.ID+":fixed-inputs")).String())
			if task.Recovered && cmd.Submitted {
				task.RecoveryInputs = []taskflow.AttachmentLocation{{Filename: pins[0].Filename, Path: path.Join(root, "1-销售 数据.xlsx")}, {Filename: pins[1].Filename, Path: path.Join(root, "2-销售 数据.xlsx")}}
			}
			mock.ExpectQuery("SELECT resource_manifest FROM work_tasks").WithArgs(cmd.TaskID).
				WillReturnRows(sqlmock.NewRows([]string{"resources"}).AddRow(mustJSON(pins)))
			shouldStage := !cmd.Submitted && (cmd.Turn == 1 || task.Recovered)
			if shouldStage {
				for _, pin := range pins {
					mock.ExpectQuery("SELECT storage_key,sha256,size FROM work_resource_versions").WithArgs(pin.VersionID).
						WillReturnRows(sqlmock.NewRows([]string{"key", "digest", "size"}).AddRow("inputs/"+pin.VersionID.String(), pin.SHA256, 100))
				}
			}
			err := c.stageWorkInputs(context.Background(), env, cmd, &task)
			if scenario == "failed-download" {
				if err == nil || task.Text != text {
					t.Fatalf("failed staging advertised files: %v %s", err, task.Text)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if task.Text != text {
					t.Fatalf("staging rewrote the user's message: %s", task.Text)
				}
				for _, absent := range []string{pins[0].Filename, "/workspace/input", pins[0].VersionID.String(), pins[1].VersionID.String(), pins[0].SHA256, "version_id", "sha256", "description", cmd.ID, current.Filename, current.URL} {
					if strings.Contains(task.Text, absent) {
						t.Fatalf("internal metadata or unstaged round attachment leaked: %q", absent)
					}
				}
				if task.Recovered {
					prompt := executionPrompt(task)
					for _, filename := range []string{"1-销售 数据.xlsx", "2-销售 数据.xlsx"} {
						if strings.Count(prompt, path.Join(root, filename)) != 1 {
							t.Fatalf("recovery receipt missing or duplicated: %s", prompt)
						}
					}
				} else if strings.Contains(executionPrompt(task), "已恢复的可用附件") {
					t.Fatal("ordinary round received a recovery inventory")
				}
			}
			if task.InputText() != text || task.Attachments[0] != current {
				t.Fatal("raw user text or current attachment changed")
			}
			if shouldStage {
				request := <-server.requests
				if !request.WorkMode || len(request.Attachments) != len(pins) {
					t.Fatal("fixed attachment staging lost files")
				}
				for i, pin := range pins {
					if request.Attachments[i].Filename != pin.Filename || request.Attachments[i].SHA256 != pin.SHA256 {
						t.Fatal("advertised filenames differ from staged inputs")
					}
				}
			} else if len(server.requests) != 0 {
				t.Fatal("ordinary continuation or submitted retry staged old files")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRecoveryInventoryRejectsIncompleteOrForeignReceipts(t *testing.T) {
	for _, invalid := range []string{"missing", "name", "path"} {
		t.Run(invalid, func(t *testing.T) {
			server := &workInputStagingServer{requests: make(chan workInputStageRequest, 1), badReceipt: invalid}
			c, _ := workInputTestClient(t, server)
			task := taskflow.CreateTaskReq{ID: uuid.New(), Env: map[string]string{"JINGJIAAGENT_WORK_MODE": "1"}, Attachments: []taskflow.Attachment{{Filename: "data.txt", URL: "https://files.invalid/data"}}}
			if _, err := c.engine("node").attachmentLocations(context.Background(), "sandbox", Command{ID: uuid.NewString()}, task); err == nil {
				t.Fatal("unverified location accepted into recovery inventory")
			}
		})
	}
}
