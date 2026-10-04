package runtimeadapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"github.com/google/uuid"
)

func TestGitCredentialBridgeReconfigurationKeepsSingleScopedHelper(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("real Git required")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("Linux shell required")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Git fixture failed: %v: %s", err, out)
		}
		return string(out)
	}
	git("init", "-q")
	git("config", "--local", "core.quotepath", "false")
	git("config", "--local", "--add", "credential.helper", "old-helper-one")
	git("config", "--local", "--add", "credential.helper", "old-helper-two")

	// File transport is isolated; the exact Git command emitted by the
	// adapter executes against a real temporary repository on every replay.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SandboxID string `json:"sandboxId"`
			Command   struct {
				Args []string `json:"args"`
			} `json:"command"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Command.Args) != 2 || req.SandboxID != "git-sandbox" {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		command := req.Command.Args[1]
		stdout, exit := "", 0
		if strings.HasPrefix(command, "git config ") {
			cmd := exec.CommandContext(r.Context(), "sh", "-c", command)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			stdout = string(out)
			if err != nil {
				exit = 1
			}
		} else {
			parts := strings.Split(command, "'")
			if len(parts) < 3 {
				http.Error(w, "invalid fixture file request", 400)
				return
			}
			data, err := base64.StdEncoding.DecodeString(parts[len(parts)-2])
			var file struct {
				Op string `json:"op"`
			}
			if err != nil || json.Unmarshal(data, &file) != nil {
				http.Error(w, "invalid fixture file payload", 400)
				return
			}
			stdout = `{"data":null}`
			if file.Op == "begin" {
				stdout = `{"data":{"temp":"fixture-upload"}}`
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"exitCode": exit, "stdout": stdout}})
	}))
	defer server.Close()
	l := testLedger(t)
	ctx := context.Background()
	owner := uuid.NewString()
	env := Environment{ID: "agent_" + uuid.NewString(), OwnerID: owner, NodeID: "git-test-node",
		Request: taskflow.CreateVirtualMachineReq{Git: taskflow.Git{URL: "https://git.example.test/team/repo.git"}}}
	if err := l.SaveEnvironment(ctx, env); err != nil {
		t.Fatal(err)
	}
	if err := l.SetEnvironment(ctx, env.ID, "git-project", "git-sandbox", "online"); err != nil {
		t.Fatal(err)
	}
	env.SandboxID = "git-sandbox"
	client := &Client{ledger: l, engines: map[string]*Engine{"git-test-node": {
		exec: rpc.NewExecServiceClient(server.Client(), server.URL, connect.WithProtoJSON()),
	}}, gitCredentialURL: "https://product.example.test/api/v1/runtime/git-credential"}
	task := taskflow.CreateTaskReq{ID: uuid.New(), LLM: taskflow.LLM{ApiKey: "isolated-test-token"}}
	expected := "\n!python3 /data/state/monkeycode-git/" + task.ID.String() + ".py\n"
	for i := 0; i < 3; i++ {
		if err := client.writeGitCredentialBridge(ctx, env, task); err != nil {
			t.Fatalf("credential configuration replay %d failed: %v", i, err)
		}
		if actual := git("config", "--local", "--get-all", "credential.helper"); actual != expected {
			t.Fatalf("unexpected actual Git helper chain after replay %d: %q", i, actual)
		}
	}
	if actual := git("config", "--local", "core.quotepath"); actual != "false\n" {
		t.Fatal("credential reconfiguration changed unrelated repository settings")
	}
}
