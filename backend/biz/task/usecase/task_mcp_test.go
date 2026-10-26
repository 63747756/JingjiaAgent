package usecase

import (
	"strings"
	"testing"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/google/uuid"
)

func TestTaskMCPUsesAuthenticatedPlatformGateway(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.BaseURL = "https://agent.example.test/"
	a := &TaskUsecase{cfg: cfg}
	servers := a.buildMCPConfigs(uuid.New(), "task-token")
	if len(servers) != 1 || servers[0].Name != "jingjiaagent" || servers[0].Url == nil || *servers[0].Url != "https://agent.example.test/mcp" {
		t.Fatalf("unexpected MCP servers: %+v", servers)
	}
	if len(servers[0].Headers) != 1 || servers[0].Headers[0].Name != "Authorization" || servers[0].Headers[0].Value != "Bearer task-token" {
		t.Fatal("task gateway authorization missing")
	}
	if strings.Contains(*servers[0].Url, "65510") {
		t.Fatal("unavailable legacy builtin server remains")
	}
	if len(a.buildMCPConfigs(uuid.New(), "")) != 0 {
		t.Fatal("unauthenticated task received builtin tools")
	}
}
