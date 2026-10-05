package runtimeadapter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/config"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type execTestServer struct {
	rpc.UnimplementedExecServiceHandler
	truncated bool
	t         *testing.T
}

func (s *execTestServer) Exec(ctx context.Context, r *connect.Request[v2.ExecRequest]) (*connect.Response[v2.ExecResponse], error) {
	if r.Header().Get("Authorization") != "Bearer test-token" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing bearer token"))
	}
	if r.Msg.GetSandboxId() != "sandbox" || r.Msg.Command.Command != "sh" || len(r.Msg.Command.Args) != 2 || r.Msg.Command.Args[0] != "-c" {
		s.t.Fatal("Exec contract changed")
	}
	return connect.NewResponse(&v2.ExecResponse{Result: &v2.ExecResult{Stdout: "中文", StdoutTruncated: s.truncated}}), nil
}
func testEngine(t *testing.T, mux *http.ServeMux) *Engine {
	t.Helper()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("test-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(config.RuntimeNode{URL: server.URL, TokenFile: token})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}
func TestAuthenticatedExecAndTruncation(t *testing.T) {
	service := &execTestServer{t: t}
	mux := http.NewServeMux()
	path, h := rpc.NewExecServiceHandler(service)
	mux.Handle(path, h)
	e := testEngine(t, mux)
	out, err := e.execute(context.Background(), "sandbox", "printf test", 100)
	if err != nil || out != "中文" {
		t.Fatalf("Exec failed: %s %v", out, err)
	}
	service.truncated = true
	if _, err = e.execute(context.Background(), "sandbox", "printf test", 1); err == nil {
		t.Fatal("truncated output accepted as complete")
	}
}
