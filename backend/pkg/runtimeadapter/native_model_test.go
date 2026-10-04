package runtimeadapter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"github.com/google/uuid"
)

type nativeModelFiles struct {
	rpc.UnimplementedExecServiceHandler
	mu         sync.Mutex
	files      map[string][]byte
	pending    map[string][]byte
	failCommit bool
	aborted    int
}

func (s *nativeModelFiles) Exec(_ context.Context, request *connect.Request[v2.ExecRequest]) (*connect.Response[v2.ExecResponse], error) {
	if request.Msg.GetSandboxId() != "retained-sandbox" || len(request.Msg.Command.Args) != 2 {
		return nil, errors.New("invalid retained Sandbox request")
	}
	parts := strings.Split(request.Msg.Command.Args[1], "'")
	if len(parts) < 2 {
		return nil, errors.New("invalid file request")
	}
	raw, err := base64.StdEncoding.DecodeString(parts[len(parts)-2])
	if err != nil {
		return nil, err
	}
	var input struct {
		Op, Path, Temp string
		Data           []byte
		Parents        bool
		Offset, Size   int
		Mode           uint32
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var output any = map[string]any{}
	switch input.Op {
	case "home":
		output = map[string]string{"path": "/data/home"}
	case "begin":
		if !input.Parents || !strings.HasPrefix(input.Path, "/data/state/monkeycode-native/") {
			return nil, errors.New("invalid native model path")
		}
		s.pending[input.Path] = nil
		output = map[string]string{"temp": input.Path + ".tmp"}
	case "write":
		if input.Temp != input.Path+".tmp" || len(s.pending[input.Path]) != input.Offset {
			return nil, errors.New("invalid file offset")
		}
		s.pending[input.Path] = append(s.pending[input.Path], input.Data...)
	case "commit":
		if input.Mode != 0600 || input.Size != len(s.pending[input.Path]) {
			return nil, errors.New("invalid native model mode or size")
		}
		if s.failCommit && strings.HasSuffix(input.Path, ".model.json") {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("fixture upload failure"))
		}
		s.files[input.Path] = s.pending[input.Path]
		delete(s.pending, input.Path)
	case "abort":
		delete(s.pending, input.Path)
		s.aborted++
	default:
		return nil, errors.New("unexpected file operation")
	}
	return connect.NewResponse(&v2.ExecResponse{Result: &v2.ExecResult{Stdout: string(mustJSON(map[string]any{"data": output}))}}), nil
}

func nativeModelFixture(t *testing.T) (*Client, Environment, *nativeModelFiles, func(int)) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		_ = db.Close()
	})
	ledger, err := NewLedger(db, bytes.Repeat([]byte("x"), 32))
	if err != nil {
		t.Fatal(err)
	}
	env := Environment{ID: "env", OwnerID: "owner", NodeID: "node", SandboxID: "retained-sandbox", State: "online",
		Request: taskflow.CreateVirtualMachineReq{Envs: []string{"MONKEYCODE_MODEL_API_KEY=sandbox-old-key", "MONKEYCODE_MODEL_BASE_URL=https://old.example", "MONKEYCODE_MODEL_NAME=old-model"}}}
	sealed, err := ledger.seal(env.ID, mustJSON(env.Request))
	if err != nil {
		t.Fatal(err)
	}
	expectLookups := func(count int) {
		for i := 0; i < count; i++ {
			mock.ExpectQuery("SELECT id,owner_id,node_id,project_id,sandbox_id,state,created_at,payload FROM runtime_environments").WithArgs(env.ID).
				WillReturnRows(sqlmock.NewRows([]string{"id", "owner_id", "node_id", "project_id", "sandbox_id", "state", "created_at", "payload"}).
					AddRow(env.ID, env.OwnerID, env.NodeID, "project", env.SandboxID, env.State, time.Unix(1, 0), sealed))
		}
	}
	files := &nativeModelFiles{files: map[string][]byte{}, pending: map[string][]byte{}}
	mux := http.NewServeMux()
	path, handler := rpc.NewExecServiceHandler(files)
	mux.Handle(path, handler)
	return &Client{ledger: ledger, engines: map[string]*Engine{"node": testEngine(t, mux)}}, env, files, expectLookups
}

func TestNativeModelConfigRefreshesRetainedSandboxWithoutConfigFiles(t *testing.T) {
	for _, provider := range []struct {
		name  string
		agent taskflow.CodingAgent
	}{{"claude", taskflow.CodingAgentClaude}, {"codex", taskflow.CodingAgentCodex}} {
		t.Run(provider.name, func(t *testing.T) {
			client, env, files, expectLookups := nativeModelFixture(t)
			task := taskflow.CreateTaskReq{ID: uuid.New(), VMID: env.ID, CodingAgent: provider.agent}
			for _, model := range []taskflow.LLM{
				{ApiKey: "first-key", BaseURL: "https://first.example/v1", Model: "first-model"},
				{ApiKey: "replacement-key", BaseURL: "https://replacement.example/v1", Model: "replacement-model"},
			} {
				if err := mergeExecution(&task, &taskflow.TaskExecutionConfig{LLM: &model}); err != nil {
					t.Fatal(err)
				}
				// A later update with no LLM preserves the accepted intent;
				// stale environment overrides cannot replace its model pair.
				if err := mergeExecution(&task, &taskflow.TaskExecutionConfig{Envs: map[string]string{"MONKEYCODE_MODEL_API_KEY": "stale-key"}}); err != nil {
					t.Fatal(err)
				}
				expectLookups(3) // config setup, atomic model upload, empty rule selection
				if err := client.writeTaskConfigs(context.Background(), env, task); err != nil {
					t.Fatal(err)
				}
				var actual map[string]string
				files.mu.Lock()
				raw := bytes.Clone(files.files["/data/state/monkeycode-native/"+task.ID.String()+".model.json"])
				files.mu.Unlock()
				if err := json.Unmarshal(raw, &actual); err != nil {
					t.Fatal(err)
				}
				want := map[string]string{"task_id": task.ID.String(), "api_key": model.ApiKey, "base_url": model.BaseURL, "model": model.Model}
				if !reflect.DeepEqual(actual, want) {
					t.Fatal("native model did not use the latest complete LLM configuration")
				}
			}
		})
	}
}

func TestNativeModelConfigOverridesCallerSuppliedConfigFile(t *testing.T) {
	client, env, files, expectLookups := nativeModelFixture(t)
	task := taskflow.CreateTaskReq{ID: uuid.New(), VMID: env.ID, CodingAgent: taskflow.CodingAgentClaude,
		LLM: taskflow.LLM{ApiKey: "current-key", BaseURL: "https://current.example", Model: "current-model"}}
	path := "/data/state/monkeycode-native/" + task.ID.String() + ".model.json"
	task.Configs = []taskflow.ConfigFile{{Path: path, Content: `{"api_key":"stale-key"}`}}
	expectLookups(4)
	if err := client.writeTaskConfigs(context.Background(), env, task); err != nil {
		t.Fatal(err)
	}
	files.mu.Lock()
	defer files.mu.Unlock()
	var actual map[string]string
	if err := json.Unmarshal(files.files[path], &actual); err != nil {
		t.Fatal(err)
	}
	if actual["api_key"] != task.LLM.ApiKey || actual["base_url"] != task.LLM.BaseURL || actual["model"] != task.LLM.Model || actual["task_id"] != task.ID.String() {
		t.Fatal("caller-supplied file replaced the controlled model selection")
	}
}

func TestNativeModelConfigUploadFailureStopsConfiguration(t *testing.T) {
	client, env, files, expectLookups := nativeModelFixture(t)
	task := taskflow.CreateTaskReq{ID: uuid.New(), VMID: env.ID, CodingAgent: taskflow.CodingAgentClaude,
		LLM: taskflow.LLM{ApiKey: "replacement-key", BaseURL: "https://replacement.example", Model: "replacement-model"}}
	path := "/data/state/monkeycode-native/" + task.ID.String() + ".model.json"
	files.files[path] = []byte("previous model")
	files.failCommit = true
	expectLookups(2)
	if err := client.writeTaskConfigs(context.Background(), env, task); err == nil {
		t.Fatal("Run configuration accepted a failed model upload")
	}
	files.mu.Lock()
	defer files.mu.Unlock()
	if string(files.files[path]) != "previous model" || len(files.files) != 1 || len(files.pending) != 0 || files.aborted != 1 {
		t.Fatal("failed model upload was not aborted atomically")
	}
}
