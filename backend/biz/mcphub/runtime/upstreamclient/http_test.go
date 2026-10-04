package upstreamclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chaitin/MonkeyCode/backend/biz/mcphub/repo"
	"github.com/chaitin/MonkeyCode/backend/biz/mcphub/runtime/gateway"
	"github.com/google/uuid"
)

type testRPC struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func replyRPC(w io.Writer, request testRPC, result any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
}

func initializeResult() any {
	return map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "test", "version": "1"}}
}

func testUpstream(server *httptest.Server) *repo.UpstreamConfig {
	return &repo.UpstreamConfig{ID: uuid.New(), URL: server.URL, Headers: map[string]string{"Authorization": "Bearer test-only", "Accept": "custom/type"}}
}

func TestPersistentSSEReturnsMatchingResponseBeforeEOF(t *testing.T) {
	var closed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			closed.Add(1)
			w.WriteHeader(204)
			return
		}
		if !strings.Contains(r.Header.Get("Accept"), "application/json") || !strings.Contains(r.Header.Get("Accept"), "text/event-stream") || r.Header.Get("Authorization") != "Bearer test-only" {
			t.Error("required MCP headers missing")
		}
		var req testRPC
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "initialize":
			if r.Header.Get("Mcp-Session-Id") != "" {
				t.Error("initialize inherited session")
			}
			w.Header().Set("Mcp-Session-Id", "owned-session")
			w.Header().Set("Content-Type", "application/json")
			replyRPC(w, req, initializeResult())
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") != "owned-session" || r.Header.Get("Mcp-Protocol-Version") != "2025-03-26" {
				t.Error("negotiated session missing")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, ": keep alive\r\nevent: message\r\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\r\n\r\n")
			_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"id\":\"unrelated\",\"result\":{}}\n\n")
			data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []any{map[string]any{"name": "proof"}}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}
	}))
	defer server.Close()
	start := time.Now()
	up := testUpstream(server)
	up.Headers["Mcp-Session-Id"] = "must-not-inherit"
	tools, err := NewHTTPClient(2*time.Second, false).ListTools(context.Background(), up)
	if err != nil || len(tools) != 1 || tools[0].Name != "proof" {
		t.Fatalf("persistent SSE failed: %v", err)
	}
	if time.Since(start) > time.Second || closed.Load() != 1 {
		t.Fatal("waited for EOF or failed to terminate own session")
	}
}

func TestConcurrentOperationsOwnSessions(t *testing.T) {
	const count = 8
	var mu sync.Mutex
	sessions := map[string]string{}
	requests := map[string]bool{}
	ready := make(chan struct{})
	var initialized int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(204)
			return
		}
		var req testRPC
		_ = json.NewDecoder(r.Body).Decode(&req)
		owner := r.Header.Get("X-Owner")
		if req.Method == "initialize" {
			sid := uuid.NewString()
			mu.Lock()
			sessions[sid] = owner
			if requests[req.ID] {
				t.Error("initialize request ID reused")
			}
			requests[req.ID] = true
			mu.Unlock()
			w.Header().Set("Mcp-Session-Id", sid)
			replyRPC(w, req, initializeResult())
			return
		}
		mu.Lock()
		valid := sessions[r.Header.Get("Mcp-Session-Id")] == owner
		mu.Unlock()
		if !valid {
			w.WriteHeader(409)
			return
		}
		if req.Method == "notifications/initialized" {
			mu.Lock()
			initialized++
			if initialized == count {
				close(ready)
			}
			mu.Unlock()
			select {
			case <-ready:
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(202)
			return
		}
		mu.Lock()
		if requests[req.ID] {
			t.Error("tool request ID reused")
		}
		requests[req.ID] = true
		mu.Unlock()
		replyRPC(w, req, map[string]any{"content": []any{map[string]string{"type": "text", "text": owner}}})
	}))
	defer server.Close()
	client := NewHTTPClient(3*time.Second, false)
	id := uuid.New()
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			owner := fmt.Sprint(i)
			up := &repo.UpstreamConfig{ID: id, URL: server.URL, Headers: map[string]string{"X-Owner": owner}}
			result, _, err := client.CallTool(context.Background(), up, repo.ToolSnapshot{}, gateway.CallToolParams{Name: "proof", Arguments: json.RawMessage("{}")})
			if err != nil || !strings.Contains(string(result), "\"text\":\""+owner+"\"") {
				t.Errorf("operation %d used another session: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
}

func TestLegacySSEFallback(t *testing.T) {
	messages := make(chan []byte, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && r.Method == http.MethodPost {
			w.WriteHeader(405)
			return
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: endpoint\ndata: /messages?session=owned\n\n")
			w.(http.Flusher).Flush()
			for {
				select {
				case data := <-messages:
					_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		}
		if r.URL.Path != "/messages" || r.URL.Query().Get("session") != "owned" || r.Header.Get("Authorization") != "Bearer test-only" {
			w.WriteHeader(403)
			return
		}
		var req testRPC
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(202)
		if req.ID == "" {
			return
		}
		result := initializeResult()
		if req.Method == "tools/list" {
			result = map[string]any{"tools": []any{map[string]string{"name": "legacy-proof"}}}
		}
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		messages <- data
	}))
	defer server.Close()
	tools, err := NewHTTPClient(2*time.Second, false).ListTools(context.Background(), testUpstream(server))
	if err != nil || len(tools) != 1 || tools[0].Name != "legacy-proof" {
		t.Fatalf("legacy fallback failed: %v", err)
	}
}

func TestFailuresDoNotReplayOrForwardCredentials(t *testing.T) {
	for _, mode := range []string{"auth", "initialize-rpc", "version", "mismatch", "timeout", "expired-session", "legacy-origin", "redirect", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			var calls, gets, leaked atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1); w.WriteHeader(200) }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					w.WriteHeader(204)
					return
				}
				if r.Method == http.MethodGet {
					gets.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", target.URL)
					return
				}
				var req testRPC
				_ = json.NewDecoder(r.Body).Decode(&req)
				if req.Method == "initialize" {
					switch mode {
					case "auth":
						w.WriteHeader(401)
						return
					case "initialize-rpc":
						_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -1, "message": "rejected"}})
						return
					case "version":
						replyRPC(w, req, map[string]string{"protocolVersion": "unknown"})
						return
					case "legacy-origin":
						w.WriteHeader(405)
						return
					case "redirect":
						w.Header().Set("Location", target.URL)
						w.WriteHeader(307)
						return
					}
					replyRPC(w, req, initializeResult())
					return
				}
				if req.ID == "" {
					w.WriteHeader(202)
					return
				}
				calls.Add(1)
				switch mode {
				case "timeout":
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				case "expired-session":
					w.WriteHeader(404)
				case "oversize":
					_, _ = io.WriteString(w, strings.Repeat("x", maxMCPResponse+1))
				default:
					replyRPC(w, testRPC{ID: "wrong"}, map[string]any{})
				}
			}))
			defer server.Close()
			_, _, err := NewHTTPClient(300*time.Millisecond, false).CallTool(context.Background(), testUpstream(server), repo.ToolSnapshot{}, gateway.CallToolParams{Name: "proof", Arguments: json.RawMessage("{}")})
			if err == nil || calls.Load() > 1 || leaked.Load() != 0 {
				t.Fatal("failure accepted, tool replayed, or credentials forwarded")
			}
			if mode != "legacy-origin" && gets.Load() != 0 {
				t.Fatal("non-transport failure switched transport")
			}
		})
	}
}

func TestSSEResponseMultilineBatchAndLimits(t *testing.T) {
	input := ": comment\n\ndata: [{\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\"},\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"wanted\",\"result\":{}}]\n\n"
	result, err := newSSEReader(strings.NewReader(input)).response("wanted")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rpcResult(result, "wanted"); err != nil {
		t.Fatal(err)
	}
	escaped := []byte(`{"jsonrpc":"2.0","id":"\u0077anted","result":{}}`)
	if _, err := rpcResult(escaped, "wanted"); err != nil {
		t.Fatal("equivalent JSON string ID was rejected")
	}
	if !matchesRPCID(json.RawMessage(`"\u0077anted"`), "wanted") || matchesRPCID(json.RawMessage("1"), "1") {
		t.Fatal("request IDs must match JSON string values, including escapes")
	}
	for _, input := range []string{"data: {}\n", "data: " + strings.Repeat("x", maxMCPResponse) + "\n\n"} {
		if _, err := newSSEReader(strings.NewReader(input)).response("wanted"); err == nil {
			t.Fatal("truncated or oversized SSE accepted")
		}
	}
}
