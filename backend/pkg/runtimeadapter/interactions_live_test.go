package runtimeadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func liveInteractions(t *testing.T, ctx context.Context, c *Client, taskID uuid.UUID, vm, owner string, restart func(bool, *taskflow.TaskExecutionConfig) *taskflow.RestartTaskResp, waitTurn func(int, string), output func(int) []byte) {
	t.Helper()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	assertAbsent := func(name string) {
		t.Helper()
		files, err := c.FileManager().Operate(ctx, taskflow.FileReq{ID: vm, UserID: owner, Path: "/workspace", Operate: taskflow.FileOpList})
		check(err)
		for _, file := range files {
			if file.Name == name {
				t.Fatalf("unexpected file %s", name)
			}
		}
	}
	waitRequest := func(turn int, kind string) nativeInteraction {
		t.Helper()
		deadline := time.Now().Add(time.Minute)
		for time.Now().Before(deadline) && ctx.Err() == nil {
			err := c.Step(ctx)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				check(err)
			}
			var data []byte
			err = c.ledger.db.QueryRowContext(ctx, `SELECT chunk FROM runtime_events WHERE task_id=$1 AND turn=$2 AND source_key LIKE 'interaction/%' ORDER BY seq DESC LIMIT 1`, taskID, turn).Scan(&data)
			if err == nil {
				var chunk taskflow.TaskChunk
				var body struct {
					ToolCall struct {
						Input nativeInteraction `json:"rawInput"`
					} `json:"toolCall"`
				}
				check(json.Unmarshal(data, &chunk))
				check(json.Unmarshal(chunk.Data, &body))
				if body.ToolCall.Input.Kind != kind {
					t.Fatal("wrong native interaction type")
				}
				return body.ToolCall.Input
			}
			if !errors.Is(err, sql.ErrNoRows) {
				check(err)
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatal("real Agent did not produce a pending native interaction")
		return nativeInteraction{}
	}
	answer := func(native nativeInteraction, choice string) taskflow.AskUserQuestionResponse {
		t.Helper()
		request := taskflow.AskUserQuestionResponse{TaskId: taskID.String(), RequestId: native.ID, AnswersJson: string(mustJSON(map[string]string{native.Questions[0].Question: choice}))}
		done := make(chan error, 1)
		go func() { done <- c.TaskManager().AskUserQuestion(ctx, request) }()
		for ctx.Err() == nil {
			select {
			case err := <-done:
				check(err)
				return request
			default:
			}
			err := c.Step(ctx)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				check(err)
			}
			time.Sleep(100 * time.Millisecond)
		}
		check(ctx.Err())
		return request
	}
	choice := "选择_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: `Use the native question tool now. Ask exactly one question "交互验收选择" with header "验收" and options "暂不执行" and "` + choice + `". Do not use other tools. Wait for the user answer, then reply with only the selected label.`}}))
	question := waitRequest(9, "question")
	wrong := taskflow.AskUserQuestionResponse{TaskId: taskID.String(), RequestId: "que_wrong_task_request", AnswersJson: `{"交互验收选择":"暂不执行"}`}
	if c.TaskManager().AskUserQuestion(ctx, wrong) == nil {
		t.Fatal("unrelated native request ID was accepted")
	}
	request := answer(question, choice)
	waitTurn(9, "complete")
	if !strings.Contains(string(output(9)), choice) {
		t.Fatal("native Agent did not resume with actual user answer")
	}
	check(c.TaskManager().AskUserQuestion(ctx, request))
	request.AnswersJson = `{"交互验收选择":"暂不执行"}`
	if c.TaskManager().AskUserQuestion(ctx, request) == nil {
		t.Fatal("conflicting question response replay was accepted")
	}
	t.Log("real native question paused, resumed with selected answer, and replay/conflict checks passed")
	// Native configuration requires actual approval before Bash can run.
	restart(true, &taskflow.TaskExecutionConfig{ConfigFiles: []taskflow.ConfigFile{{Path: "~/.config/opencode/opencode.json", Content: `{"permission":{"bash":"ask"}}`}}})
	marker := "APPROVED_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: `Use bash once to execute exactly: printf '` + marker + `' > /workspace/approval-proof.txt . Wait for approval. Do not use any other tools or retry if rejected. Then reply APPROVAL_DONE.`}}))
	permission := waitRequest(10, "permission")
	assertAbsent("approval-proof.txt")
	answer(permission, "允许一次")
	waitTurn(10, "complete")
	var content []byte
	check(c.FileManager().Download(ctx, taskflow.FileReq{ID: vm, UserID: owner, Path: "/workspace/approval-proof.txt"}, func(_ uint64, data []byte) error { content = append(content, data...); return nil }))
	if string(content) != marker {
		t.Fatal("native approval did not execute the authorized command")
	}
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: `Use bash once to execute exactly: printf 'MUST_NOT_EXIST' > /workspace/rejected-proof.txt . Wait for approval. Do not use any other tools or retry if rejected. If rejected reply PERMISSION_REJECTED.`}}))
	permission = waitRequest(11, "permission")
	answer(permission, "拒绝")
	waitTurn(11, "complete")
	assertAbsent("rejected-proof.txt")
	t.Log("native Bash waited for explicit one-time approval; a second request was denied without executing")
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: `Use the native question tool to ask "取消等待验收" with two options "继续" and "退出". Wait for the user; do not use any other tools.`}}))
	question = waitRequest(12, "question")
	check(c.TaskManager().Cancel(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID}}))
	waitTurn(12, "canceled")
	if c.TaskManager().AskUserQuestion(ctx, taskflow.AskUserQuestionResponse{TaskId: taskID.String(), RequestId: question.ID, AnswersJson: `{"取消等待验收":"继续"}`}) == nil {
		t.Fatal("expired canceled question accepted")
	}
	t.Log("cancel during a native question reached terminal status; stale answer was rejected")
	policy := func(enabled bool) {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			done <- c.TaskManager().AutoApprove(ctx, taskflow.TaskApproveReq{ID: taskID, AutoApprove: &enabled})
		}()
		for ctx.Err() == nil {
			select {
			case err := <-done:
				check(err)
				return
			default:
			}
			if err := c.Step(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
				check(err)
			}
			time.Sleep(100 * time.Millisecond)
		}
		check(ctx.Err())
	}
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: `Use bash once to execute exactly: printf 'AUTO_ENABLED' > /workspace/auto-approved-proof.txt . Do not use other tools. Then reply AUTO_APPROVAL_DONE.`}}))
	waitRequest(13, "permission")
	assertAbsent("auto-approved-proof.txt")
	policy(true)
	waitTurn(13, "complete")
	content = nil
	check(c.FileManager().Download(ctx, taskflow.FileReq{ID: vm, UserID: owner, Path: "/workspace/auto-approved-proof.txt"}, func(_ uint64, data []byte) error { content = append(content, data...); return nil }))
	if string(content) != "AUTO_ENABLED" {
		t.Fatal("explicit auto approval did not release the waiting native request")
	}
	// Reset the native session: task policy must persist independently of the
	// provider session pointer and continue to apply to the next Run.
	restart(false, nil)
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: `Use bash once to execute exactly: printf 'AUTO_RETAINED' > /workspace/auto-retained-proof.txt . Do not use other tools. Then reply AUTO_RETAINED_DONE.`}}))
	waitTurn(14, "complete")
	content = nil
	check(c.FileManager().Download(ctx, taskflow.FileReq{ID: vm, UserID: owner, Path: "/workspace/auto-retained-proof.txt"}, func(_ uint64, data []byte) error { content = append(content, data...); return nil }))
	if string(content) != "AUTO_RETAINED" {
		t.Fatal("task auto approval policy was lost across native session reset")
	}
	policy(false)
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: `Use bash once to execute exactly: printf 'MUST_NOT_EXIST' > /workspace/auto-disabled-proof.txt . Wait for approval. Do not use any other tools or retry if rejected. If rejected reply AUTO_DISABLED_OK.`}}))
	permission = waitRequest(15, "permission")
	assertAbsent("auto-disabled-proof.txt")
	answer(permission, "拒绝")
	waitTurn(15, "complete")
	assertAbsent("auto-disabled-proof.txt")
	policy(true)
	restart(true, &taskflow.TaskExecutionConfig{ConfigFiles: []taskflow.ConfigFile{{Path: "~/.config/opencode/opencode.json", Content: `{"permission":{"bash":"deny"}}`}}})
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: `Use bash to execute: printf 'MUST_NOT_EXIST' > /workspace/explicit-deny-proof.txt . If bash is unavailable or denied, reply EXPLICIT_DENY_OK. Do not use other tools or work around a denied tool.`}}))
	waitTurn(16, "complete")
	assertAbsent("explicit-deny-proof.txt")
	var requests int
	check(c.ledger.db.QueryRowContext(ctx, `SELECT count(*) FROM runtime_events WHERE task_id=$1 AND turn=16 AND source_key LIKE 'interaction/%'`, taskID).Scan(&requests))
	if requests != 0 {
		t.Fatal("explicit deny was converted into an approvable request")
	}
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: `Use the native question tool to ask "自动审批不回答用户问题" with two options "继续" and "退出". Wait for the user; do not use other tools.`}}))
	waitRequest(17, "question")
	check(c.TaskManager().Cancel(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID}}))
	waitTurn(17, "canceled")
	t.Log("explicit auto approval released a pending permission, persisted across session reset, disabled back to manual approval, preserved native deny rules, and never answered user questions")
}
