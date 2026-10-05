package runtimeadapter

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/google/uuid"
)

func TestStoppedTerminalListDoesNotWakeButExplicitConnectionResumes(t *testing.T) {
	server := &runTestServer{sandboxStopped: true, status: v2.RunStatus_RUN_STATUS_SUCCEEDED}
	c, req := workerFixture(t, server)
	ctx := context.Background()
	list, err := c.VirtualMachiner().TerminalList(ctx, req.VMID)
	if err != nil || len(list) != 0 || server.resumes != 0 {
		t.Fatal("stopped Guest listing failed or woke the Guest", err)
	}
	shell, err := c.VirtualMachiner().Terminal(ctx, &taskflow.TerminalReq{ID: req.VMID, TerminalID: uuid.NewString()})
	if err != nil || shell == nil || server.resumes != 1 || server.starts != 0 {
		t.Fatal("explicit terminal did not resume without replaying an Agent Run", err)
	}
	server.sandboxStopped = true
	if _, err := c.ledger.db.Exec(`UPDATE runtime_environments SET state='stopping' WHERE id=$1`, req.VMID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.VirtualMachiner().Terminal(ctx, &taskflow.TerminalReq{ID: req.VMID, TerminalID: uuid.NewString()}); err == nil || server.resumes != 1 {
		t.Fatal("recycling Guest was resumed")
	}
}

func testLiveTerminal(t *testing.T, ctx context.Context, c *Client, vm string) {
	t.Helper()
	tid := uuid.NewString()
	readUntil := func(shell taskflow.Sheller, marker string) {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			output := ""
			found := false
			err := shell.BlockRead(func(d taskflow.TerminalData) {
				output += string(d.Data)
				if !found && strings.Contains(output, marker) {
					found = true
					// Stop performs an RPC, so keep the reader callback unblocked.
					go shell.Stop()
				}
			})
			if !found {
				done <- fmt.Errorf("expected PTY marker missing: %v", err)
			} else {
				done <- nil
			}
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("PTY closed before expected output: %v", err)
			}
		case <-time.After(15 * time.Second):
			shell.Stop()
			t.Fatal("PTY output did not arrive through runtime Exec")
		}
	}
	shell, err := c.VirtualMachiner().Terminal(ctx, &taskflow.TerminalReq{ID: vm, TerminalID: tid, TerminalSize: taskflow.TerminalSize{Row: 24, Col: 80}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(shell.Stop)
	if err = shell.Write(taskflow.TerminalData{Data: []byte("export PTY_TOKEN=detached; printf 'PTY_%s\\n' LIVE\n"), Resize: &taskflow.TerminalSize{Row: 31, Col: 101}}); err != nil {
		t.Fatal(err)
	}
	readUntil(shell, "PTY_LIVE")
	list, err := c.VirtualMachiner().TerminalList(ctx, vm)
	if err != nil || len(list) != 1 || list[0].ID != tid {
		t.Fatalf("detached PTY was lost: %v", err)
	}
	readonly, err := c.VirtualMachiner().Terminal(ctx, &taskflow.TerminalReq{ID: vm, TerminalID: tid, Mode: taskflow.TerminalModeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	if readonly.Write(taskflow.TerminalData{Data: []byte("touch read-only-escape\n")}) == nil {
		t.Fatal("read-only PTY accepted input")
	}
	readonly.Stop()
	shell, err = c.VirtualMachiner().Terminal(ctx, &taskflow.TerminalReq{ID: vm, TerminalID: tid, TerminalSize: taskflow.TerminalSize{Row: 31, Col: 101}})
	if err != nil {
		t.Fatal(err)
	}
	if err = shell.Write(taskflow.TerminalData{Data: []byte("printf 'RESUME_%s\\n' \"$PTY_TOKEN\"; stty size\n")}); err != nil {
		t.Fatal(err)
	}
	readUntil(shell, "RESUME_detached")
	if err = c.VirtualMachiner().CloseTerminal(ctx, &taskflow.CloseTerminalReq{ID: vm, TerminalID: tid}); err != nil {
		t.Fatal(err)
	}
	list, err = c.VirtualMachiner().TerminalList(ctx, vm)
	if err != nil || len(list) != 0 {
		t.Fatalf("closed PTY still listed: %v", err)
	}
	t.Log("real PTY resize, detach/reconnect with shell state, read-only denial and explicit close passed")
}
