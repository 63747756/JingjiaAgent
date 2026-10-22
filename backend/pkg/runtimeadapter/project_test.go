package runtimeadapter

import (
	"testing"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
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

func TestSelectedModelUsesGenericProtocolForEveryNativeCLI(t *testing.T) {
	for _, agent := range []taskflow.CodingAgent{taskflow.CodingAgentOpenCode, taskflow.CodingAgentCodex, taskflow.CodingAgentClaude} {
		for api, protocol := range map[string]string{"openai_chat": "chat_completions", "openai_responses": "responses", "anthropic": "anthropic_messages"} {
			aliases := []string{"OPENAI_API_KEY", "CODEX_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "DEEPSEEK_API_KEY", "OPENROUTER_API_KEY", "AZURE_OPENAI_API_KEY"}
			envs := map[string]string{}
			for _, key := range aliases {
				envs[key] = "conflicting-project-credential"
			}
			spec, err := projectSpec(Environment{ID: "agent-test"}, taskflow.CreateTaskReq{
				CodingAgent: agent, Env: envs,
				LLM: taskflow.LLM{ApiType: api, ApiKey: "selected-test-key", BaseURL: "http://internal-proxy:8080/v1", Model: "selected-model"},
			}, "test-image")
			if err != nil {
				t.Fatal(err)
			}
			actual := map[string]string{}
			for _, value := range spec.Agents[0].Env {
				actual[value.Name] = value.Value
				if !value.Secret {
					t.Fatal("model declaration exposed as nonsecret runtime environment")
				}
			}
			for _, key := range aliases {
				if _, exists := actual[key]; exists {
					t.Fatalf("vendor declaration can override selected %s protocol for CLI %v: %s", api, agent, key)
				}
			}
			if actual["LLM_API_KEY"] != "selected-test-key" || actual["LLM_API_ENDPOINT"] != "http://internal-proxy:8080/v1" || actual["LLM_MODEL"] != "selected-model" || actual["LLM_API_PROTOCOL"] != protocol {
				t.Fatalf("selected upstream declaration lost for CLI %v API %s", agent, api)
			}
			if actual["JINGJIAAGENT_MODEL_API_KEY"] != actual["LLM_API_KEY"] || actual["JINGJIAAGENT_MODEL_BASE_URL"] != actual["LLM_API_ENDPOINT"] {
				t.Fatal("native compatibility credentials differ from selected model")
			}
		}
	}
}

func TestProjectBusinessEnvironmentAndBranchReachEveryExecutor(t *testing.T) {
	for _, cli := range []taskflow.CodingAgent{taskflow.CodingAgentOpenCode, taskflow.CodingAgentCodex, taskflow.CodingAgentClaude} {
		env := Environment{ID: "project-env", Request: taskflow.CreateVirtualMachineReq{Git: taskflow.Git{URL: "https://git.example/repo.git", Branch: "feature/report"}, Envs: []string{"BUILD_TARGET=office-and-code", "SERVICE_URL=http://intranet.test", "JINGJIAAGENT_TASK_ID=wrong-task", "AGENT_COMPOSE_RUN_ID=wrong-run", "LLM_API_KEY=wrong-key"}}}
		task := taskflow.CreateTaskReq{CodingAgent: cli, LLM: taskflow.LLM{ApiKey: "selected-key"}, Env: map[string]string{"JINGJIAAGENT_WORK_MODE": "1"}}
		spec, err := projectSpec(env, task, "test-image")
		if err != nil {
			t.Fatal(err)
		}
		values := map[string]string{}
		for _, value := range spec.Agents[0].Env {
			values[value.Name] = value.Value
		}
		if values["BUILD_TARGET"] != "office-and-code" || values["SERVICE_URL"] != "http://intranet.test" || values["LLM_API_KEY"] != "selected-key" || values["AGENT_COMPOSE_RUN_ID"] != "" || values["JINGJIAAGENT_TASK_ID"] == "wrong-task" || spec.Agents[0].Workspace.Ref != "feature/report" {
			t.Fatalf("lost configuration for %v: %#v", cli, spec.Agents[0])
		}
	}
}
