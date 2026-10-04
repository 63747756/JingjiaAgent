package runtimeadapter

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/google/uuid"
)

//go:embed guest/terminal.py
var terminalScript []byte

// Only an explicit terminal connection resumes a stopped Guest. Power uses
// the environment lock and rejects concurrent recycle; it does not replay
// any existing Agent Run.
func (v *vmClient) readyTerminal(ctx context.Context, env Environment, engine *Engine) error {
	sandbox, err := engine.sandboxes.GetSandbox(ctx, connect.NewRequest(&v2.GetSandboxRequest{SandboxId: env.SandboxID}))
	if err != nil {
		return err
	}
	if sandbox.Msg.Sandbox == nil {
		return errors.New("runtime returned no sandbox")
	}
	if sandbox.Msg.Sandbox.Status == v2.SandboxStatus_SANDBOX_STATUS_STOPPED {
		return v.power(ctx, env, engine, "online")
	}
	if sandbox.Msg.Sandbox.Status != v2.SandboxStatus_SANDBOX_STATUS_RUNNING {
		return errors.New("runtime sandbox is not ready for a terminal")
	}
	return nil
}

func (e *Engine) terminal(ctx context.Context, sandbox string, req any, out any) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	source := base64.StdEncoding.EncodeToString(terminalScript)
	command := "python3 -c \"import base64;BROKER_SOURCE=base64.b64decode('" + source + "');exec(BROKER_SOURCE)\" '" + base64.StdEncoding.EncodeToString(data) + "'"
	if len(command) > 120000 {
		return errors.New("terminal request exceeds safe argument size")
	}
	result, err := e.execute(ctx, sandbox, command, 2<<20)
	if err != nil {
		return err
	}
	var response struct {
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if json.Unmarshal([]byte(result), &response) != nil {
		return errors.New("invalid Guest terminal response")
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	if out != nil {
		return json.Unmarshal(response.Data, out)
	}
	return nil
}

type guestShell struct {
	engine                        *Engine
	sandbox, terminalID, clientID string
	readonly                      bool
	ctx                           context.Context
	cancel                        context.CancelFunc
	once                          sync.Once
	offset                        uint64
}

func newGuestShell(ctx context.Context, e *Engine, sandbox string, r *taskflow.TerminalReq) (taskflow.Sheller, error) {
	if r.Mode != taskflow.TerminalModeReadWrite && r.Mode != taskflow.TerminalModeReadOnly {
		return nil, errors.New("invalid terminal access mode")
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &guestShell{engine: e, sandbox: sandbox, terminalID: r.TerminalID, clientID: uuid.NewString(), readonly: r.Mode == taskflow.TerminalModeReadOnly, ctx: ctx, cancel: cancel}
	var attached struct {
		Offset uint64 `json:"offset"`
	}
	err := e.terminal(ctx, sandbox, map[string]any{"op": "attach", "terminal_id": s.terminalID, "client_id": s.clientID, "readonly": s.readonly, "exec": r.Exec, "row": r.Row, "col": r.Col}, &attached)
	if err != nil {
		cancel()
		return nil, err
	}
	s.offset = attached.Offset
	return s, nil
}

func (s *guestShell) Write(data taskflow.TerminalData) error {
	if s.readonly {
		return errors.New("terminal is read-only")
	}
	if len(data.Data) > 32768 {
		return errors.New("terminal input exceeds limit")
	}
	return s.engine.terminal(s.ctx, s.sandbox, map[string]any{"op": "write", "terminal_id": s.terminalID, "client_id": s.clientID, "request_id": uuid.NewString(), "data": data.Data, "resize": data.Resize}, nil)
}

func (s *guestShell) Stop() {
	s.once.Do(func() {
		s.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Detach is distinct from the explicit CloseTerminal operation.
		_ = s.engine.terminal(ctx, s.sandbox, map[string]any{"op": "detach", "terminal_id": s.terminalID, "client_id": s.clientID}, nil)
	})
}

func (s *guestShell) BlockRead(fn func(taskflow.TerminalData)) error {
	fn(taskflow.TerminalData{Connected: true})
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		var result struct {
			Data   []byte `json:"data"`
			Offset uint64 `json:"offset"`
			Closed bool   `json:"closed"`
		}
		err := s.engine.terminal(s.ctx, s.sandbox, map[string]any{"op": "read", "terminal_id": s.terminalID, "client_id": s.clientID, "offset": s.offset}, &result)
		if err != nil {
			return err
		}
		if result.Offset != s.offset+uint64(len(result.Data)) {
			return errors.New("terminal returned an inconsistent output offset")
		}
		s.offset = result.Offset
		if len(result.Data) > 0 {
			fn(taskflow.TerminalData{Data: result.Data})
		}
		if result.Closed {
			fn(taskflow.TerminalData{Connected: false})
			return nil
		}
		if len(result.Data) == 32768 {
			continue
		}
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-ticker.C:
		}
	}
}

var _ taskflow.Sheller = (*guestShell)(nil)
