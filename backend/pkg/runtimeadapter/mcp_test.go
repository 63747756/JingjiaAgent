package runtimeadapter

import (
	"bytes"
	"context"
	"testing"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func TestRuntimeMCPOriginPreservesCredentialsAndUserEndpoints(t *testing.T) {
	public, private, external, command := "https://web.example/mcp", "https://runtime.example/mcp", "https://user.example/mcp", "node"
	c := &Client{builtinMCPURL: public, agentMCPURL: private}
	input := []taskflow.McpServerConfig{
		{Name: "jingjiaagent", Type: "http", Url: &public, Headers: []*taskflow.McpHttpHeader{{Name: "Authorization", Value: "Bearer task-bound-key"}}},
		{Name: "user-service", Type: "http", Url: &public},
		{Name: "jingjiaagent", Type: "http", Url: &external},
		{Name: "jingjiaagent", Type: "stdio", Command: &command, Url: &public},
	}
	got := c.runtimeMCPConfigs(input)
	if *got[0].Url != private || got[0].Headers[0].Value != "Bearer task-bound-key" || *input[0].Url != public {
		t.Fatal("built-in gateway routing changed task credentials or source request")
	}
	for index := 1; index < len(input); index++ {
		if got[index].Url != input[index].Url || got[index].Command != input[index].Command {
			t.Fatal("user MCP configuration was rerouted")
		}
	}
	c.agentMCPURL = ""
	if c.runtimeMCPConfigs(input)[0].Url != input[0].Url {
		t.Fatal("default gateway routing changed")
	}
}

func TestRuntimeMCPAdmissionKeepsResolvedGatewayAfterConfigReload(t *testing.T) {
	l := testLedger(t)
	ctx := context.Background()
	taskID, environmentID := uuid.New(), "agent_"+uuid.NewString()
	public := "http://127.0.0.1:47420/mcp"
	c := &Client{ledger: l, builtinMCPURL: public, agentMCPURL: "https://first.example/mcp", engines: map[string]*Engine{"node": {}}}
	if err := l.SaveEnvironment(ctx, Environment{ID: environmentID, OwnerID: uuid.NewString(), NodeID: "node", Request: taskflow.CreateVirtualMachineReq{ID: environmentID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.ExecContext(ctx, `INSERT INTO tasks(id,status) VALUES($1,'pending')`, taskID); err != nil {
		t.Fatal(err)
	}
	request := taskflow.CreateTaskReq{ID: taskID, VMID: environmentID, McpConfigs: []taskflow.McpServerConfig{
		{Name: "jingjiaagent", Type: "http", Url: &public, Headers: []*taskflow.McpHttpHeader{{Name: "Authorization", Value: "Bearer private-task-key"}}},
	}}
	for index := 0; index < 2; index++ {
		if accepted, err := c.StageTask(ctx, request); !accepted || err != nil {
			t.Fatalf("durable admission failed: %v", err)
		}
	}
	c.agentMCPURL = "https://second.example/mcp"
	prepared, err := c.PreparedTask(ctx, taskID.String())
	if err != nil || prepared == nil || *prepared.McpConfigs[0].Url != "https://first.example/mcp" {
		t.Fatal("worker reload changed the committed gateway")
	}
	if err = c.TaskManager().Create(ctx, *prepared); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err = l.db.QueryRowContext(ctx, `SELECT payload FROM runtime_task_intents WHERE task_id=$1`, taskID).Scan(&payload); err != nil || bytes.Contains(payload, []byte("private-task-key")) {
		t.Fatal("gateway credential was not encrypted")
	}
	if _, err = c.StageTask(ctx, request); err == nil {
		t.Fatal("changed gateway was silently replayed across the original admission")
	}
	var count int
	if err = l.db.QueryRowContext(ctx, `SELECT count(*) FROM runtime_commands WHERE task_id=$1`, taskID).Scan(&count); err != nil || count != 2 {
		t.Fatal("gateway reload created a second admission or task command")
	}
}
