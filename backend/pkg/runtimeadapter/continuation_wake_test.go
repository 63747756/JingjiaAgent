package runtimeadapter

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/DATA-DOG/go-sqlmock"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type continuationWakeServer struct {
	rpc.UnimplementedSandboxServiceHandler
	stopped  bool
	resumes  atomic.Int32
	onResume func() error
}

func (s *continuationWakeServer) GetSandbox(context.Context, *connect.Request[v2.GetSandboxRequest]) (*connect.Response[v2.GetSandboxResponse], error) {
	status := v2.SandboxStatus_SANDBOX_STATUS_RUNNING
	if s.stopped {
		status = v2.SandboxStatus_SANDBOX_STATUS_STOPPED
	}
	return connect.NewResponse(&v2.GetSandboxResponse{Sandbox: &v2.Sandbox{SandboxId: "sandbox", Status: status}}), nil
}

func (s *continuationWakeServer) ResumeSandbox(context.Context, *connect.Request[v2.ResumeSandboxRequest]) (*connect.Response[v2.ResumeSandboxResponse], error) {
	s.resumes.Add(1)
	if err := s.onResume(); err != nil {
		return nil, err
	}
	return connect.NewResponse(&v2.ResumeSandboxResponse{Sandbox: &v2.Sandbox{SandboxId: "sandbox", Status: v2.SandboxStatus_SANDBOX_STATUS_RUNNING}}), nil
}

func TestContinuationWakePhaseReflectsSandboxState(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		stopped, canceled, leaseLost bool
		resumeFails                  bool
		wantResumes                  int32
		wantErr                      error
	}{
		{name: "online prepares without waking"},
		{name: "stopped wakes then prepares", stopped: true, wantResumes: 1},
		{name: "canceled cannot wake", stopped: true, canceled: true, wantErr: errCanceledBeforeAdmission},
		{name: "expired worker cannot wake", stopped: true, leaseLost: true, wantErr: errLeaseLost},
		{name: "failed resume returns for preparation retry", stopped: true, resumeFails: true, wantResumes: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger, mock := recoveryLedger(t)
			ledger.workEnabled = true
			cmd := Command{ID: "command", Lease: "lease", Turn: 2}
			expectPhase := func(phase string) {
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT COALESCE\\(lease_token").WithArgs(cmd.ID, cmd.Lease).WillReturnRows(sqlmock.NewRows([]string{"valid"}).AddRow(!tc.leaseLost))
				if tc.leaseLost {
					mock.ExpectRollback()
					return
				}
				mock.ExpectQuery("SELECT cancel_requested FROM runtime_commands").WithArgs(cmd.ID).WillReturnRows(sqlmock.NewRows([]string{"canceled"}).AddRow(tc.canceled))
				if tc.canceled {
					mock.ExpectRollback()
					return
				}
				mock.ExpectExec("UPDATE runtime_commands SET preparation_phase=").WithArgs(cmd.ID, phase).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			if tc.stopped {
				expectPhase("waking")
			} else {
				expectPhase("starting")
			}
			server := &continuationWakeServer{stopped: tc.stopped, onResume: func() error {
				// Waking must be durable before the RPC, and starting follows it.
				if err := mock.ExpectationsWereMet(); err != nil {
					return err
				}
				if tc.resumeFails {
					return connect.NewError(connect.CodeUnavailable, errors.New("resume unavailable"))
				}
				expectPhase("starting")
				return nil
			}}
			mux := http.NewServeMux()
			path, handler := rpc.NewSandboxServiceHandler(server)
			mux.Handle(path, handler)
			client := &Client{ledger: ledger}
			err := client.prepareTaskSandbox(context.Background(), Environment{SandboxID: "sandbox"}, cmd, testEngine(t, mux))
			if tc.resumeFails {
				if connect.CodeOf(err) != connect.CodeUnavailable {
					t.Fatalf("expected resume failure, got %v", err)
				}
			} else if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
			if got := server.resumes.Load(); got != tc.wantResumes {
				t.Fatalf("expected %d resume calls, got %d", tc.wantResumes, got)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
