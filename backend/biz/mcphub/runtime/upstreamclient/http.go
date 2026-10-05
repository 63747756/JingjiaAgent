package upstreamclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/63747756/jingjiaagent/backend/biz/mcphub/repo"
	"github.com/63747756/jingjiaagent/backend/biz/mcphub/runtime/gateway"
	"github.com/63747756/jingjiaagent/backend/pkg/netguard"
	"github.com/google/uuid"
)

const maxMCPResponse = 8 << 20

type HTTPClient struct {
	client  *http.Client
	guard   *netguard.Guard
	timeout time.Duration
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type listToolsResult struct {
	Tools []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
	} `json:"tools"`
}

func NewHTTPClient(timeout time.Duration, blockPrivateNetwork ...bool) *HTTPClient {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	block := true
	if len(blockPrivateNetwork) > 0 {
		block = blockPrivateNetwork[0]
	}
	guard := netguard.New(block)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &HTTPClient{guard: guard, timeout: timeout, client: guard.HTTPClient(&http.Client{
		Timeout: timeout, Transport: transport,
		// Never forward configured secrets to a redirected MCP origin.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	})}
}

func (c *HTTPClient) CallTool(ctx context.Context, upstream *repo.UpstreamConfig, tool repo.ToolSnapshot, params gateway.CallToolParams) (json.RawMessage, string, error) {
	return c.call(ctx, upstream, "tools/call", map[string]any{"name": params.Name, "arguments": params.Arguments})
}

func (c *HTTPClient) ListTools(ctx context.Context, upstream *repo.UpstreamConfig) ([]repo.UpstreamTool, error) {
	data, _, err := c.call(ctx, upstream, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var result listToolsResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode upstream tools result: %w", err)
	}
	tools := make([]repo.UpstreamTool, 0, len(result.Tools))
	for _, tool := range result.Tools {
		inputSchema := tool.InputSchema
		if len(inputSchema) == 0 {
			inputSchema = json.RawMessage(`{}`)
		}
		tools = append(tools, repo.UpstreamTool{Name: tool.Name, Description: tool.Description, InputSchema: inputSchema})
	}
	return tools, nil
}

// Each operation owns its initialization, session and connection. The original
// client initialized every time too, but cached the ID across concurrent calls.
// Never retry tools/call: a lost response does not prove that execution failed.
func (c *HTTPClient) call(ctx context.Context, upstream *repo.UpstreamConfig, method string, params any) (json.RawMessage, string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := c.guard.ValidateURL(ctx, upstream.URL); err != nil {
		return nil, "", err
	}
	s := &mcpSession{client: c, upstream: upstream}
	defer s.close()
	initializeID := uuid.NewString()
	initParams := map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "mcphub", "version": "1.0.0"}}
	data, headers, status, err := s.post(ctx, upstream.URL, initializeID, "initialize", initParams)
	// Only a rejected initialization can select the older GET + POST transport.
	// Authentication errors, timeouts and business requests never trigger replay.
	if err == nil && (status == http.StatusNotFound || status == http.StatusMethodNotAllowed) {
		if err = s.openLegacy(ctx); err == nil {
			data, headers, status, err = s.post(ctx, s.endpoint, initializeID, "initialize", initParams)
		}
	}
	if err != nil {
		return nil, "", err
	}
	if status != http.StatusOK && !(s.stream != nil && status == http.StatusAccepted) {
		return nil, "", httpStatusError(status)
	}
	if s.stream == nil {
		// Even a rejected RPC/version can have allocated a server session.
		s.id = headers.Get("Mcp-Session-Id")
	}
	result, err := rpcResult(data, initializeID)
	if err != nil {
		return nil, "", fmt.Errorf("initialize upstream: %w", err)
	}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(result, &initialized) != nil || (initialized.ProtocolVersion != "2025-03-26" && initialized.ProtocolVersion != "2024-11-05") {
		return nil, "", errors.New("unsupported upstream MCP protocol version")
	}
	s.version = initialized.ProtocolVersion
	endpoint := upstream.URL
	if s.stream != nil {
		endpoint = s.endpoint
	}
	_, _, status, err = s.post(ctx, endpoint, "", "notifications/initialized", nil)
	if err != nil {
		return nil, "", err
	}
	if status < 200 || status >= 300 {
		return nil, "", httpStatusError(status)
	}
	id := uuid.NewString()
	data, headers, status, err = s.post(ctx, endpoint, id, method, params)
	if err != nil {
		return nil, "", err
	}
	requestID := headers.Get("X-Request-Id")
	if status != http.StatusOK && !(s.stream != nil && status == http.StatusAccepted) {
		return nil, requestID, httpStatusError(status)
	}
	result, err = rpcResult(data, id)
	return result, requestID, err
}

type mcpSession struct {
	client                *HTTPClient
	upstream              *repo.UpstreamConfig
	id, version, endpoint string
	stream                io.ReadCloser
	events                *sseReader
}

func (s *mcpSession) post(ctx context.Context, endpoint, id, method string, params any) ([]byte, http.Header, int, error) {
	payload := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != "" {
		payload["id"] = id
	}
	if params != nil {
		payload["params"] = params
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("marshal upstream request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, 0, err
	}
	applyMCPHeaders(req, s.upstream.Headers)
	// Configured headers cannot inject a different operation's protocol/session.
	req.Header.Del("Mcp-Session-Id")
	req.Header.Del("Mcp-Protocol-Version")
	if s.id != "" {
		req.Header.Set("Mcp-Session-Id", s.id)
	}
	if s.version != "" {
		req.Header.Set("Mcp-Protocol-Version", s.version)
	}
	resp, err := s.client.client.Do(req)
	if err != nil {
		return nil, nil, 0, err
	}
	defer resp.Body.Close()
	headers := resp.Header.Clone()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, headers, resp.StatusCode, nil
	}
	if id == "" {
		return nil, headers, resp.StatusCode, nil
	}
	var data []byte
	if s.events != nil && resp.StatusCode == http.StatusAccepted {
		data, err = s.events.response(id)
	} else if strings.Contains(strings.ToLower(headers.Get("Content-Type")), "text/event-stream") {
		data, err = newSSEReader(resp.Body).response(id)
	} else {
		data, err = io.ReadAll(io.LimitReader(resp.Body, maxMCPResponse+1))
		if len(data) > maxMCPResponse {
			err = errors.New("upstream response exceeds size limit")
		}
	}
	if err != nil {
		return nil, headers, resp.StatusCode, fmt.Errorf("read upstream response: %w", err)
	}
	return data, headers, resp.StatusCode, nil
}

func (s *mcpSession) close() {
	if s.stream != nil {
		_ = s.stream.Close()
		return
	}
	if s.id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.upstream.URL, nil)
	if err != nil {
		return
	}
	applyMCPHeaders(req, s.upstream.Headers)
	req.Header.Set("Mcp-Session-Id", s.id)
	req.Header.Set("Mcp-Protocol-Version", s.version)
	if resp, err := s.client.client.Do(req); err == nil {
		_ = resp.Body.Close()
	}
}

func httpStatusError(status int) error { return fmt.Errorf("upstream http %d", status) }

func rpcResult(data []byte, id string) (json.RawMessage, error) {
	var rpc rpcResponse
	if err := json.Unmarshal(data, &rpc); err != nil {
		return nil, fmt.Errorf("decode upstream response json: %w", err)
	}
	if rpc.JSONRPC != "2.0" || !matchesRPCID(rpc.ID, id) {
		return nil, errors.New("upstream response does not match request")
	}
	if rpc.Error != nil {
		return nil, errors.New(rpc.Error.Message)
	}
	if len(rpc.Result) == 0 {
		return nil, errors.New("upstream response has no result")
	}
	return rpc.Result, nil
}

func matchesRPCID(raw json.RawMessage, want string) bool {
	var got string
	return json.Unmarshal(raw, &got) == nil && got == want
}

func applyMCPHeaders(req *http.Request, headers map[string]string) {
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Accept", ensureMCPAccept(req.Header.Get("Accept")))
}

func ensureMCPAccept(current string) string {
	parts := make([]string, 0, 4)
	seen := make(map[string]bool)
	for part := range strings.SplitSeq(current, ",") {
		part = strings.TrimSpace(part)
		if part != "" && !seen[part] {
			parts = append(parts, part)
			seen[part] = true
		}
	}
	for _, required := range []string{"application/json", "text/event-stream"} {
		if !seen[required] {
			parts = append(parts, required)
		}
	}
	return strings.Join(parts, ", ")
}
