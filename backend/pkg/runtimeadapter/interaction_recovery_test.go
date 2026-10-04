package runtimeadapter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"github.com/google/uuid"
)

func recoveryLedger(t *testing.T) (*Ledger, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ledger, err := NewLedger(db, bytes.Repeat([]byte{13}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return ledger, mock
}

func expectRecoveryEnvironment(t *testing.T, ledger *Ledger, mock sqlmock.Sqlmock, task string) {
	t.Helper()
	payload, err := ledger.seal("env", mustJSON(taskflow.CreateVirtualMachineReq{ID: "env"}))
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("SELECT environment_id FROM runtime_task_intents").WithArgs(task).WillReturnRows(sqlmock.NewRows([]string{"environment_id"}).AddRow("env"))
	mock.ExpectQuery("SELECT id,owner_id,node_id,project_id,sandbox_id,state,created_at,payload").WithArgs("env").WillReturnRows(sqlmock.NewRows([]string{"id", "owner_id", "node_id", "project_id", "sandbox_id", "state", "created_at", "payload"}).AddRow("env", "owner", "node", "project", "sandbox", "online", time.Now(), payload))
}

type interactionRecoveryServer struct {
	rpc.UnimplementedExecServiceHandler
	result      string
	execErr     error
	receiptOnly atomic.Bool
}

func (s *interactionRecoveryServer) Exec(_ context.Context, r *connect.Request[v2.ExecRequest]) (*connect.Response[v2.ExecResponse], error) {
	parts := strings.Split(r.Msg.Command.Args[1], "'")
	raw, err := base64.StdEncoding.DecodeString(parts[len(parts)-2])
	if err != nil {
		return nil, err
	}
	var request struct {
		ReceiptOnly bool `json:"receipt_only"`
	}
	if err = json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	s.receiptOnly.Store(request.ReceiptOnly)
	if s.execErr != nil {
		return nil, s.execErr
	}
	return connect.NewResponse(&v2.ExecResponse{Result: &v2.ExecResult{Stdout: s.result}}), nil
}

func TestTerminalInteractionRecoversReceiptOrExpiresWithoutApproval(t *testing.T) {
	for _, test := range []struct {
		name, result, state string
		success             bool
	}{
		{"native receipt", `{"data":{"success":true}}`, "complete", true},
		{"no receipt", `{"data":{"success":false,"expired":true}}`, "canceled", false},
		{"unreadable proof", `{"error":"Guest interaction requires reconciliation"}`, "canceled", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger, mock := recoveryLedger(t)
			server := &interactionRecoveryServer{result: test.result}
			mux := http.NewServeMux()
			path, handler := rpc.NewExecServiceHandler(server)
			mux.Handle(path, handler)
			payload := interactionCommand{Target: "run-command", Native: nativeInteraction{ID: "que_proof", RunID: "run-1", SessionID: "session-1", Kind: "question"}, Request: taskflow.AskUserQuestionResponse{TaskId: "task", RequestId: "que_proof", AnswersJson: `{"confirm":"yes"}`}, Turn: 2}
			command := Command{ID: "reply", TaskID: "task", Lease: "lease", Submitted: true, Payload: mustJSON(payload), Operation: "interaction"}
			mock.ExpectQuery("SELECT run_id,state FROM runtime_commands").WithArgs("run-command", "task").WillReturnRows(sqlmock.NewRows([]string{"run_id", "state"}).AddRow("run-1", "canceled"))
			if test.success {
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT COALESCE\\(lease_token").WithArgs("reply", "lease").WillReturnRows(sqlmock.NewRows([]string{"valid"}).AddRow(true))
				mock.ExpectExec("INSERT INTO runtime_events").WithArgs("task", "reply", 2, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectExec("UPDATE runtime_commands SET state='complete'").WithArgs("reply").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			} else {
				// No reply-question event is inserted for an unproven answer.
				mock.ExpectExec("UPDATE runtime_commands SET state='canceled',result=\\$3").WithArgs("reply", "lease", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			if err := (&Client{ledger: ledger}).processInteraction(context.Background(), &command, Environment{SandboxID: "sandbox"}, testEngine(t, mux)); err != nil {
				t.Fatal(err)
			}
			if !server.receiptOnly.Load() {
				t.Fatal("terminal recovery may repeat a native side effect")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFinishDefersTerminalUntilInteractionSettles(t *testing.T) {
	ledger, mock := recoveryLedger(t)
	command := Command{ID: "run", TaskID: "task", EnvironmentID: "env", Lease: "lease", Operation: "task", RunID: "remote-run", Turn: 3, Offset: 19}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT task_id FROM runtime_task_intents").WithArgs("task").WillReturnRows(sqlmock.NewRows([]string{"task_id"}).AddRow("task"))
	mock.ExpectQuery("SELECT sandbox_id FROM runtime_environments").WithArgs("env").WillReturnRows(sqlmock.NewRows([]string{"sandbox_id"}).AddRow("sandbox"))
	mock.ExpectQuery("SELECT COALESCE\\(lease_token").WithArgs("run", "lease").WillReturnRows(sqlmock.NewRows([]string{"valid"}).AddRow(true))
	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM runtime_commands.*operation='interaction'").WithArgs("task").WillReturnRows(sqlmock.NewRows([]string{"pending"}).AddRow(true))
	mock.ExpectExec("UPDATE runtime_commands SET state='running',result=jsonb_build_object").WithArgs("run", "remote-run", int64(19)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := ledger.Finish(context.Background(), command, "complete", ""); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLiveRecoveryHoldsLegacyTerminalForLateReceipt(t *testing.T) {
	ledger, mock := recoveryLedger(t)
	expectRecoveryEnvironment(t, ledger, mock, "task")
	end := mustJSON(taskflow.TaskChunk{Event: "task-ended", Timestamp: 20})
	reply := mustJSON(taskflow.TaskChunk{Event: "reply-question", Timestamp: 30})
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT seq,turn,chunk FROM runtime_events").WithArgs("task", uint64(0)).WillReturnRows(sqlmock.NewRows([]string{"seq", "turn", "chunk"}).AddRow(2, 1, end))
	mock.ExpectQuery("SELECT EXISTS").WithArgs("task").WillReturnRows(sqlmock.NewRows([]string{"pending"}).AddRow(true))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT seq,turn,chunk FROM runtime_events").WithArgs("task", uint64(2)).WillReturnRows(sqlmock.NewRows([]string{"seq", "turn", "chunk"}).AddRow(3, 1, reply))
	mock.ExpectQuery("SELECT EXISTS").WithArgs("task").WillReturnRows(sqlmock.NewRows([]string{"pending"}).AddRow(false))
	mock.ExpectCommit()
	var got []string
	err := (&Client{ledger: ledger, poll: time.Millisecond}).TaskLiveAfter(context.Background(), "task", 0, func(chunk *taskflow.TaskChunk) error { got = append(got, chunk.Event); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"reply-question", "task-ended"}) {
		t.Fatalf("receipt swallowed by terminal: %v", got)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRestartWaitFailureRemainsDurablePending(t *testing.T) {
	ledger, mock := recoveryLedger(t)
	mock.ExpectQuery("SELECT state,result FROM runtime_commands").WithArgs("restart").WillReturnError(context.DeadlineExceeded)
	_, err := (&taskClient{c: &Client{ledger: ledger}}).waitRestart(context.Background(), "restart")
	if !taskflow.IsRestartPending(err) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost pending status: %v", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQuestionAdmissionRejectsObservedTerminalWhileReceiptSettles(t *testing.T) {
	ledger, mock := recoveryLedger(t)
	request := taskflow.AskUserQuestionResponse{TaskId: "task", RequestId: "que_late", AnswersJson: `{"confirm":"yes"}`}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT task_id FROM runtime_task_intents").WithArgs("task").WillReturnRows(sqlmock.NewRows([]string{"task_id"}).AddRow("task"))
	mock.ExpectQuery("SELECT payload FROM runtime_commands").WillReturnRows(sqlmock.NewRows([]string{"payload"}))
	mock.ExpectQuery("SELECT e.chunk,c.id,c.run_id,c.state,c.turn").WithArgs("task", "interaction/que_late").WillReturnRows(sqlmock.NewRows([]string{"chunk", "id", "run_id", "state", "turn", "terminal"}).AddRow([]byte(`{}`), "run", "remote-run", "running", 1, true))
	mock.ExpectRollback()
	err := (&taskClient{c: &Client{ledger: ledger}}).answer(context.Background(), Environment{ID: "env"}, request)
	if err == nil || !strings.Contains(err.Error(), "no longer active") {
		t.Fatalf("late answer admitted: %v", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLiveRecoveryNeverLeaksNextTurnBeforeClosing(t *testing.T) {
	ledger, mock := recoveryLedger(t)
	expectRecoveryEnvironment(t, ledger, mock, "task")
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT seq,turn,chunk FROM runtime_events").WithArgs("task", uint64(0)).WillReturnRows(sqlmock.NewRows([]string{"seq", "turn", "chunk"}).
		AddRow(2, 1, mustJSON(taskflow.TaskChunk{Event: "task-ended"})).
		AddRow(3, 1, mustJSON(taskflow.TaskChunk{Event: "reply-question"})).
		AddRow(4, 2, mustJSON(taskflow.TaskChunk{Event: "user-input"})))
	// The pending interaction belongs to the newer admitted turn, so it must
	// neither hold the older terminal nor leak into that older subscription.
	mock.ExpectQuery("SELECT EXISTS").WithArgs("task").WillReturnRows(sqlmock.NewRows([]string{"pending"}).AddRow(true))
	mock.ExpectCommit()
	var got []string
	err := (&Client{ledger: ledger, poll: time.Millisecond}).TaskLiveAfter(context.Background(), "task", 0, func(chunk *taskflow.TaskChunk) error { got = append(got, chunk.Event); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"reply-question", "task-ended"}) {
		t.Fatalf("wrong turn delivery: %v", got)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStoppedSandboxSettlesOnlyConfirmedTerminalInteraction(t *testing.T) {
	for _, test := range []struct {
		name              string
		stopped, terminal bool
	}{
		{"terminal and stopped", true, true},
		{"terminal but reachable running sandbox", false, true},
		{"live run cannot be expired", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger, mock := recoveryLedger(t)
			execution := &interactionRecoveryServer{execErr: connect.NewError(connect.CodeFailedPrecondition, errors.New("sandbox unavailable"))}
			status := v2.RunStatus_RUN_STATUS_RUNNING
			if test.terminal {
				status = v2.RunStatus_RUN_STATUS_CANCELED
			}
			runs := &runTestServer{status: status, sandboxStopped: test.stopped}
			mux := http.NewServeMux()
			path, handler := rpc.NewExecServiceHandler(execution)
			mux.Handle(path, handler)
			path, handler = rpc.NewRunServiceHandler(runs)
			mux.Handle(path, handler)
			path, handler = rpc.NewSandboxServiceHandler(runs)
			mux.Handle(path, handler)
			payload := interactionCommand{Target: "target", Native: nativeInteraction{RunID: "run-1", ID: "per_proof", SessionID: "session", Kind: "permission"}, Answers: [][]string{{"允许一次"}}}
			command := Command{ID: "reply", TaskID: "task", Lease: "lease", Operation: "interaction", Submitted: true, Payload: mustJSON(payload)}
			mock.ExpectQuery("SELECT run_id,state FROM runtime_commands").WithArgs("target", "task").WillReturnRows(sqlmock.NewRows([]string{"run_id", "state"}).AddRow("run-1", "running"))
			if test.terminal && test.stopped {
				mock.ExpectExec("UPDATE runtime_commands SET state='canceled',result=\\$3").WithArgs("reply", "lease", `{"reason":"terminal_sandbox_stopped","receipt_status":"unconfirmed"}`).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			err := (&Client{ledger: ledger}).processInteraction(context.Background(), &command, Environment{SandboxID: "sandbox-1"}, testEngine(t, mux))
			if (err == nil) != (test.terminal && test.stopped) {
				t.Fatalf("unexpected convergence: %v", err)
			}
			if execution.receiptOnly.Load() != test.terminal {
				t.Fatal("wrong native recovery mode")
			}
			if runs.resumes != 0 {
				t.Fatal("recovery revived a fenced Guest")
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResumeRestartUsesPersistedResultWithoutRuntimeNode(t *testing.T) {
	ledger, mock := recoveryLedger(t)
	request := taskflow.RestartTaskReq{ID: uuid.New(), RequestId: "original", LoadSession: true}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(request.ID.String()+":restart:"+request.RequestId)).String()
	sealed, err := ledger.seal(id, mustJSON(restartCommand{Request: request}))
	if err != nil {
		t.Fatal(err)
	}
	expectRecoveryEnvironment(t, ledger, mock, request.ID.String())
	mock.ExpectQuery("SELECT payload FROM runtime_commands").WithArgs(id, request.ID).WillReturnRows(sqlmock.NewRows([]string{"payload"}).AddRow(sealed))
	mock.ExpectQuery("SELECT state,result FROM runtime_commands").WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"state", "result"}).AddRow("complete", mustJSON(taskflow.RestartTaskResp{Success: true, SessionID: "original-session", BusinessStateCommitted: true})))
	response, found, err := (&taskClient{c: &Client{ledger: ledger}}).ResumeRestart(context.Background(), request)
	if err != nil || !found || response == nil || response.SessionID != "original-session" || !response.BusinessStateCommitted {
		t.Fatalf("could not recover offline result: %+v %v %v", response, found, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResumeRestartConflictNeverAdmitsNewWork(t *testing.T) {
	ledger, mock := recoveryLedger(t)
	request := taskflow.RestartTaskReq{ID: uuid.New(), RequestId: "original", LoadSession: true}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(request.ID.String()+":restart:"+request.RequestId)).String()
	sealed, err := ledger.seal(id, mustJSON(restartCommand{Request: request}))
	if err != nil {
		t.Fatal(err)
	}
	expectRecoveryEnvironment(t, ledger, mock, request.ID.String())
	mock.ExpectQuery("SELECT payload FROM runtime_commands").WithArgs(id, request.ID).WillReturnRows(sqlmock.NewRows([]string{"payload"}).AddRow(sealed))
	request.LoadSession = false
	_, found, err := (&taskClient{c: &Client{ledger: ledger}}).ResumeRestart(context.Background(), request)
	if err == nil || !found {
		t.Fatalf("conflicting replay not fenced: %v %v", found, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRestartBusinessFailureRollsBackRuntimeSessionAndCompletion(t *testing.T) {
	ledger, mock := recoveryLedger(t)
	task, owner, model, switchID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	payload := restartCommand{Request: taskflow.RestartTaskReq{ID: task, RequestId: "switch", BusinessMutation: &taskflow.RestartBusinessMutation{OwnerID: owner, ModelSwitch: &taskflow.RestartModelSwitch{ID: switchID, ModelID: model}}}}
	command := Command{ID: uuid.NewString(), TaskID: task.String(), EnvironmentID: "env", Lease: uuid.NewString(), Operation: "restart", Payload: mustJSON(payload)}
	intent := taskflow.CreateTaskReq{ID: task, CodingAgent: taskflow.CodingAgentOpenCode}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT cancel_requested FROM runtime_task_intents").WithArgs(task.String()).WillReturnRows(sqlmock.NewRows([]string{"cancel_requested"}).AddRow(false))
	mock.ExpectQuery("SELECT state FROM runtime_environments").WithArgs("env").WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow("online"))
	mock.ExpectQuery("SELECT COALESCE\\(lease_token").WithArgs(command.ID, command.Lease).WillReturnRows(sqlmock.NewRows([]string{"valid"}).AddRow(true))
	mock.ExpectQuery("SELECT cancel_requested FROM runtime_commands").WithArgs(command.ID).WillReturnRows(sqlmock.NewRows([]string{"cancel_requested"}).AddRow(false))
	mock.ExpectExec("UPDATE runtime_task_intents SET payload").WithArgs(task.String(), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO runtime_task_sessions").WithArgs(task.String(), "opencode", "restarted-session", command.ID).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT user_id FROM tasks").WithArgs(task.String()).WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(owner.String()))
	mock.ExpectQuery("SELECT success FROM task_model_switches").WithArgs(switchID, task.String(), owner, model).WillReturnRows(sqlmock.NewRows([]string{"success"}).AddRow(nil))
	failure := errors.New("fixture business write failure")
	mock.ExpectExec("UPDATE project_tasks SET model_id").WithArgs(task.String(), model).WillReturnError(failure)
	mock.ExpectRollback()
	err := (&Client{ledger: ledger}).finishRestart(context.Background(), command, intent, taskflow.RestartTaskResp{Success: true, SessionID: "restarted-session"})
	if !errors.Is(err, failure) {
		t.Fatalf("business write failure was acknowledged: %v", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
