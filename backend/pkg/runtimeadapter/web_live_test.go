package runtimeadapter

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/google/uuid"
)

// Opt-in contract check against the isolated Web database and actual daemon.
// It only reapplies the deterministic project spec; it never starts a Run.
func TestWebProjectContract(t *testing.T) {
	dir := os.Getenv("RUNTIME_WEB_CONFIG_DIR")
	if dir == "" {
		t.Skip("isolated Web PoC is not configured")
	}
	cfg, err := config.Init(dir)
	if err != nil {
		t.Fatal("cannot load isolated Web configuration")
	}
	c, err := NewClient(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal("cannot open isolated runtime adapter")
	}
	defer c.Close()
	ctx := context.Background()
	var id string
	if err = c.ledger.db.QueryRowContext(ctx, `SELECT task_id FROM runtime_task_intents ORDER BY task_id LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e, err := c.ledger.EnvironmentForTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.ledger.Intent(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := projectSpec(e, r, c.nodes[e.NodeID].GuestImage)
	if err != nil {
		t.Fatal(err)
	}
	response, err := c.engines[e.NodeID].projects.ApplyProject(ctx, connect.NewRequest(&v2.ApplyProjectRequest{Spec: spec}))
	if err != nil {
		message := err.Error()
		for _, secret := range []string{r.LLM.ApiKey, e.Request.Git.Token} {
			if secret != "" {
				message = strings.ReplaceAll(message, secret, "[redacted]")
			}
		}
		t.Fatalf("project contract: %s", message)
	}
	if response.Msg.GetProject().GetSummary().GetProjectId() == "" {
		for _, issue := range response.Msg.Issues {
			t.Logf("validation path: %s; message: %s", issue.Path, strings.ReplaceAll(issue.Message, r.LLM.ApiKey, "[redacted]"))
		}
		t.Fatal("project was not applied")
	}
}

// Explicitly selected local test task only. This fixture requires native Bash
// approval for the following browser scenario; it does not certify an original
// permission-settings page or add a product endpoint.
func TestWebNativeApprovalSetup(t *testing.T) {
	dir, id := os.Getenv("RUNTIME_WEB_CONFIG_DIR"), os.Getenv("RUNTIME_WEB_APPROVAL_TASK")
	if dir == "" || id == "" {
		t.Skip("set isolated Web config and approval test task")
	}
	cfg, err := config.Init(dir)
	if err != nil {
		t.Fatal("cannot read isolated Web config")
	}
	dsn, err := url.Parse(cfg.Database.Master)
	if err != nil || dsn.Hostname() != "127.0.0.1" || dsn.Path != "/monkeycode_web_poc" || cfg.Server.BaseURL != "http://127.0.0.1:47420" {
		t.Fatal("approval fixture must target only the named local PoC")
	}
	task, err := uuid.Parse(id)
	if err != nil {
		t.Fatal("invalid selected approval test task")
	}
	c, err := NewClient(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal("cannot open isolated runtime adapter")
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	intent, err := c.ledger.Intent(ctx, id)
	if err != nil || intent.CodingAgent != taskflow.CodingAgentOpenCode || !strings.Contains(intent.Text, "WEB_APPROVAL_READY") {
		t.Fatal("selected task is not the explicitly created approval fixture")
	}
	file := taskflow.ConfigFile{Path: "~/.config/opencode/opencode.json", Content: "{}"}
	for _, candidate := range intent.Configs {
		if candidate.Path == file.Path {
			file = candidate
			break
		}
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal([]byte(file.Content), &settings) != nil || settings == nil {
		t.Fatal("invalid original OpenCode config")
	}
	permission := map[string]json.RawMessage{}
	if old, exists := settings["permission"]; exists {
		if json.Unmarshal(old, &permission) != nil || permission == nil {
			t.Fatal("fixture cannot replace a scalar permission policy")
		}
	}
	permission["bash"] = json.RawMessage(`"ask"`)
	settings["permission"] = mustJSON(permission)
	file.Content = string(mustJSON(settings))
	response, err := c.TaskManager().Restart(ctx, taskflow.RestartTaskReq{ID: task, RequestId: "web-native-approval-setup-v1", LoadSession: true, ExecutionConfig: &taskflow.TaskExecutionConfig{ConfigFiles: []taskflow.ConfigFile{file}}})
	if err != nil || response == nil || !response.Success {
		t.Fatal("native approval fixture restart did not complete")
	}
	t.Log("selected isolated test task now requires native Bash approval; browser verification remains required")
}
