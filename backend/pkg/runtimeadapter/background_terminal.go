package runtimeadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"

	"connectrpc.com/connect"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/google/uuid"
)

var backgroundIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type backgroundArguments struct {
	Command    string `json:"command,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	TerminalID string `json:"terminal_id,omitempty"`
}

func parseBackgroundArguments(operation string, raw json.RawMessage) (backgroundArguments, error) {
	var args backgroundArguments
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return args, errors.New("invalid background terminal arguments")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return args, errors.New("invalid background terminal arguments")
	}
	switch operation {
	case "create":
		if !backgroundIdentifier.MatchString(args.RequestID) || strings.TrimSpace(args.Command) == "" || len(args.Command) > 16384 || strings.ContainsRune(args.Command, 0) || args.TerminalID != "" {
			return args, errors.New("create requires a command and a stable request_id (letters, digits, underscores or hyphens)")
		}
	case "list":
		if args != (backgroundArguments{}) {
			return args, errors.New("list takes no arguments")
		}
	case "output_path", "kill":
		if !backgroundIdentifier.MatchString(args.TerminalID) || args.Command != "" || args.Cwd != "" || args.RequestID != "" {
			return args, errors.New("operation requires only terminal_id")
		}
	default:
		return args, errors.New("unsupported background terminal operation")
	}
	return args, nil
}

// BackgroundTerminal is a task-scoped MCP operation. It never wakes an idle
// environment or replays a command from a previous runtime generation.
func (c *Client) BackgroundTerminal(ctx context.Context, userID, taskID uuid.UUID, operation, requestID string, raw json.RawMessage) (json.RawMessage, error) {
	args, err := parseBackgroundArguments(operation, raw)
	if err != nil {
		return nil, err
	}
	if userID == uuid.Nil || taskID == uuid.Nil {
		return nil, errors.New("task identity is required")
	}
	env, err := c.ledger.EnvironmentForTask(ctx, taskID.String())
	if err != nil {
		return nil, err
	}
	if env.OwnerID != userID.String() {
		return nil, errors.New("task environment access denied")
	}
	env, engine, err := c.environment(ctx, env.ID)
	if err != nil {
		return nil, err
	}
	if env.State != "online" {
		return nil, errors.New("工作空间当前未运行，请先恢复任务；后台命令不会自动重放")
	}
	sandbox, err := engine.sandboxes.GetSandbox(ctx, connect.NewRequest(&v2.GetSandboxRequest{SandboxId: env.SandboxID}))
	if err != nil {
		return nil, err
	}
	if sandbox.Msg.Sandbox == nil || sandbox.Msg.Sandbox.Status != v2.SandboxStatus_SANDBOX_STATUS_RUNNING {
		return nil, errors.New("工作空间当前未运行，后台终端不可用")
	}
	request := map[string]any{"op": "background_" + operation, "scope": taskID.String(), "generation": env.ID + "_" + env.SandboxID}
	if operation == "create" {
		request["command"], request["cwd"], request["request_id"] = args.Command, args.Cwd, args.RequestID
	} else if args.TerminalID != "" {
		request["terminal_id"] = args.TerminalID
	}
	var result json.RawMessage
	err = engine.terminal(ctx, env.SandboxID, request, &result)
	return result, err
}
