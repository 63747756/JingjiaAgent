package runtimeadapter

import (
	"testing"

	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
)

func TestProjectPreservesModelAndMCPProtocols(t *testing.T) {
	empty, remote, command := "", "https://mcp.example/mcp", "node"
	for api, protocol := range map[string]string{"openai_chat": "chat_completions", "openai_responses": "responses", "anthropic": "anthropic_messages"} {
		spec, err := projectSpec(Environment{ID: "agent-test", Request: taskflow.CreateVirtualMachineReq{}}, taskflow.CreateTaskReq{
			LLM: taskflow.LLM{ApiType: api}, McpConfigs: []taskflow.McpServerConfig{
				{Name: "http", Type: "http", Url: &remote, Command: &empty},
				{Name: "sse", Type: "sse", Url: &remote},
				{Name: "local", Type: "stdio", Command: &command, Args: []string{"server.js"}},
			},
		}, "test-image")
		if err != nil {
			t.Fatal(err)
		}
		a := spec.Agents[0]
		var actual string
		for _, value := range a.Env {
			if value.Name == "LLM_API_PROTOCOL" {
				actual = value.Value
			}
		}
		if actual != protocol {
			t.Fatalf("model protocol lost: %s", api)
		}
		if a.McpServers[0].Type != "remote" || a.McpServers[0].Transport != "http" || a.McpServers[1].Transport != "sse" || a.McpServers[2].Type != "local" || a.McpServers[2].Transport != "" {
			t.Fatal("MCP transport contract lost")
		}
	}
}
