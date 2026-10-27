package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/63747756/jingjiaagent/backend/biz/mcphub/auth"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// BuiltinRuntime deliberately bypasses the external-tool registry/billing:
// these operations belong to the authenticated task's own runtime.
type BuiltinRuntime interface {
	BackgroundTerminal(context.Context, uuid.UUID, uuid.UUID, string, string, json.RawMessage) (json.RawMessage, error)
	RequestPreview(context.Context, uuid.UUID, uuid.UUID, int) (json.RawMessage, error)
}

func WithBuiltinRuntime(runtime BuiltinRuntime) Option {
	return func(h *Handler) { h.builtin = runtime }
}

func builtinTools() []mcp.Tool {
	return []mcp.Tool{
		mcp.NewToolWithRawSchema("background_terminal_create", "Start a long-running server in this task's workspace. Reuse request_id when retrying the same start; use a new request_id only for an intentional restart. Do not append & or nohup: this tool owns the background session. cwd must be inside /workspace.", json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","minLength":1,"maxLength":16384},"cwd":{"type":"string","default":"/workspace"},"request_id":{"type":"string","pattern":"^[A-Za-z0-9_-]{1,128}$"}},"required":["command","request_id"],"additionalProperties":false}`)),
		mcp.NewToolWithRawSchema("background_terminal_list", "List managed background sessions for this task and current workspace generation. Listing never starts or restarts a server.", json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)),
		mcp.NewToolWithRawSchema("background_terminal_output_path", "Get a managed terminal's bounded log path. Read the file with your existing shell tools; output may also have a previous .1 log.", terminalIDSchema()),
		mcp.NewToolWithRawSchema("background_terminal_kill", "Stop only the specified task-owned background terminal and its session processes. This does not close the preview mapping.", terminalIDSchema()),
		mcp.NewToolWithRawSchema("request_preview", "Get a domain preview URL for an HTTP service already running in this task's workspace. A new route is public; existing access policies are preserved. Does not start or restore a service, and cannot reopen access closed by the employee. The preview panel also discovers HTTP services without calling this tool.", json.RawMessage(`{"type":"object","properties":{"port":{"type":"integer","minimum":1,"maximum":65535}},"required":["port"],"additionalProperties":false}`)),
	}
}

func terminalIDSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"terminal_id":{"type":"string","minLength":1,"maxLength":128}},"required":["terminal_id"],"additionalProperties":false}`)
}

func (h *Handler) isBuiltin(name string) bool {
	if h.builtin == nil {
		return false
	}
	for _, tool := range builtinTools() {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func (h *Handler) builtinServerTools() []mcpserver.ServerTool {
	if h.builtin == nil {
		return nil
	}
	var tools []mcpserver.ServerTool
	for _, tool := range builtinTools() {
		tools = append(tools, mcpserver.ServerTool{Tool: tool, Handler: func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			// Resolve identity from the current authenticated HTTP request, never
			// from the session that happened to list tools previously.
			subject, ok := SubjectFromContext(ctx)
			if !ok {
				return nil, errUnauthorized
			}
			args, err := marshalArguments(req.Params.Arguments)
			if err != nil {
				return nil, err
			}
			return h.callBuiltin(ctx, subject, req.Params.Name, args)
		}})
	}
	return tools
}

func (h *Handler) callBuiltin(ctx context.Context, subject *auth.Subject, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	if !h.isBuiltin(name) || subject == nil || subject.TaskID == uuid.Nil || subject.UserID == uuid.Nil {
		return nil, errUnauthorized
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	requestID := BuildRequestID(subject, RequestIDFromContext(ctx), name, args)
	start := time.Now()
	var result json.RawMessage
	var err error
	if name == "request_preview" {
		var fields map[string]json.RawMessage
		var port int
		if json.Unmarshal(args, &fields) != nil || len(fields) != 1 || json.Unmarshal(fields["port"], &port) != nil || port < 1 || port > 65535 {
			err = errors.New("request_preview requires only an integer port between 1 and 65535")
		} else {
			result, err = h.builtin.RequestPreview(ctx, subject.UserID, subject.TaskID, port)
		}
	} else {
		operations := map[string]string{"background_terminal_create": "create", "background_terminal_list": "list", "background_terminal_output_path": "output_path", "background_terminal_kill": "kill"}
		result, err = h.builtin.BackgroundTerminal(ctx, subject.UserID, subject.TaskID, operations[name], requestID, args)
	}
	// Do not log commands, user arguments, runtime tokens, or log contents.
	h.logger.InfoContext(ctx, "mcphub builtin call", "tool", name, "task_id", subject.TaskID, "user_id", subject.UserID, "request_id", requestID, "success", err == nil, "duration_ms", time.Since(start).Milliseconds())
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(result)), nil
}
