package runtimeadapter

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

// A real, isolated HTTP MCP service. Its tool computes a result and returns an
// unpredictable receipt, so model prose alone cannot satisfy the live check.
// This verifies runtime header delivery and execution, not product MCP scopes.
func liveMCP(t *testing.T) (taskflow.McpServerConfig, string, *atomic.Int32) {
	t.Helper()
	token := uuid.NewString()
	receipt := "MCP_RESULT_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
				Name            string `json:"name"`
				Arguments       struct {
					Value int `json:"value"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&request) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(request.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": request.Params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "isolated-runtime-probe", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "runtime_probe", "description": "Compute twice the input value and return an unpredictable acceptance receipt.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]string{"type": "integer"}}, "required": []string{"value"}}}}}
		case "tools/call":
			if request.Params.Name != "runtime_probe" || request.Params.Arguments.Value != 21 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			calls.Add(1)
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "42 " + receipt}}, "isError": false}
		case "ping":
			result = map[string]any{}
		default:
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	t.Cleanup(server.Close)
	// An unauthenticated request cannot enumerate tools or execute the probe.
	response, err := server.Client().Post(server.URL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || calls.Load() != 0 {
		t.Fatal("isolated MCP service accepted an unauthenticated request")
	}
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.Host = "host.docker.internal:" + u.Port()
	address := u.String()
	return taskflow.McpServerConfig{Name: "acceptance", Type: "http", Url: &address, Headers: []*taskflow.McpHttpHeader{{Name: "Authorization", Value: "Bearer " + token}}}, receipt, calls
}
