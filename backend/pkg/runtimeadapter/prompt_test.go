package runtimeadapter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/google/uuid"
)

func TestTaskDefaultsOnlyEnterInitialOrRecoveredUserTurn(t *testing.T) {
	text := "这次只需要中文摘要"
	task := taskflow.CreateTaskReq{Text: text, UserText: &text, PromptContext: taskflow.PromptContext{Version: taskflow.PromptContextVersion, AssistantInstructions: "专业方法", OutputRequirements: "生成英文报告", ProjectRules: "业务口径"}}
	first := executionPrompt(task, 1)
	if !strings.Contains(first, "生成英文报告") || !strings.HasSuffix(first, text) || strings.Contains(first, "专业方法") || task.InputText() != text {
		t.Fatal("initial task defaults or raw user input routed incorrectly")
	}
	if next := executionPrompt(task, 2); next != text {
		t.Fatal("initial requirements were replayed over the latest user turn", next)
	}
	task.ContextStartTurn = 3
	if reset := executionPrompt(task, 3); !strings.Contains(reset, "生成英文报告") || strings.Contains(reset, "最近对话") {
		t.Fatal("explicit context reset lost task defaults or resurrected history")
	}
	if next := executionPrompt(task, 4); next != text {
		t.Fatal("reset task defaults replayed on later turns")
	}
	task.Recovered, task.RecoveryContext = true, "用户在第二轮改为中文摘要"
	recovered := executionPrompt(task, 3)
	if strings.Count(recovered, "生成英文报告") != 1 || strings.Index(recovered, "生成英文报告") >= strings.Index(recovered, task.RecoveryContext) || !strings.HasSuffix(recovered, text) {
		t.Fatal("recovery must put initial defaults before recent history and current input")
	}
	raw, _ := json.Marshal(task)
	var retry taskflow.CreateTaskReq
	if json.Unmarshal(raw, &retry) != nil || executionPrompt(retry, 3) != recovered || retry.InputText() != text {
		t.Fatal("retry changed frozen context or public input")
	}
}

func TestCanceledFirstNativeTurnDoesNotConsumeTaskDefaults(t *testing.T) {
	for _, state := range []string{"canceled", "failed"} {
		for _, session := range []string{"", "created-before-user-submission"} {
			t.Run(state+"/"+session, func(t *testing.T) { testInterruptedFirstNativeTurn(t, state, session) })
		}
	}
}

func testInterruptedFirstNativeTurn(t *testing.T, state, initialSession string) {
	nativeSession := initialSession
	server := &runTestServer{nativeSession: &nativeSession, status: v2.RunStatus_RUN_STATUS_RUNNING}
	c, task := workerFixture(t, server)
	if c.ledger.workEnabled {
		t.Fatal("fixture must cover the ordinary coding task path")
	}
	task.ContextStartTurn = 2 // A reset has already cleared the native session.
	task.PromptContext = taskflow.PromptContext{Version: taskflow.PromptContextVersion, UserInstructions: "INITIAL_DEFAULT_ONLY"}
	ctx := context.Background()
	data := mustJSON(task)
	sealed, err := c.ledger.seal(task.ID.String(), data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ledger.db.Exec(`UPDATE runtime_task_intents SET payload=$2,payload_hash=$3 WHERE task_id=$1`, task.ID, sealed, hash(data)); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ledger.db.Exec(`UPDATE runtime_commands SET state='complete',result=$2 WHERE task_id=$1 AND operation='task'`, task.ID, string(mustJSON(map[string]string{"provider": "opencode", "session_id": initialSession}))); err != nil {
		t.Fatal(err)
	}
	continueTask := func(text string) {
		t.Helper()
		if err := c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: task.ID, Text: text, ClientMessageID: uuid.NewString()}}); err != nil {
			t.Fatal(err)
		}
	}
	continueTask("取消的新会话首条消息")
	// A native session pointer can exist even though the first user turn was
	// never accepted. Neither that pointer nor a terminal failed run is proof.
	if _, err = c.ledger.db.Exec(`UPDATE runtime_commands SET state=$2,submission_started=$3,result=$4 WHERE task_id=$1 AND operation='task' AND turn=2`, task.ID, state, initialSession != "", string(mustJSON(map[string]string{"provider": "opencode", "session_id": initialSession}))); err != nil {
		t.Fatal(err)
	}
	continueTask("实际执行的首条消息")
	if err = c.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if len(server.prompts) != 1 || strings.Count(server.prompts[0], "INITIAL_DEFAULT_ONLY") != 1 || !strings.HasSuffix(server.prompts[0], "实际执行的首条消息") {
		t.Fatalf("unsubmitted canceled turn consumed task context: %v", server.prompts)
	}
	var commandID string
	var payload []byte
	if err = c.ledger.db.QueryRow(`SELECT id,payload FROM runtime_commands WHERE task_id=$1 AND operation='task' AND turn=3`, task.ID).Scan(&commandID, &payload); err != nil {
		t.Fatal(err)
	}
	plain, err := c.ledger.open(commandID, payload)
	var frozen taskflow.CreateTaskReq
	if err != nil || json.Unmarshal(plain, &frozen) != nil || frozen.ContextStartTurn != 3 || frozen.Recovered || frozen.RecoveryContext != "" || executionPrompt(frozen, 3) != server.prompts[0] {
		t.Fatal("coding round did not freeze actual first-session context independently of history", err)
	}
	if _, err = c.ledger.db.Exec(`UPDATE runtime_commands SET state='complete',result=$2,lease_token=NULL,lease_until=NULL WHERE id=$1`, commandID, string(mustJSON(map[string]string{"provider": "opencode", "session_id": nativeSession}))); err != nil {
		t.Fatal(err)
	}
	server.requestID = ""
	continueTask("后续改为新要求")
	if err = c.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if len(server.prompts) != 2 || server.prompts[1] != "后续改为新要求" {
		t.Fatalf("live native session received old defaults again: %v", server.prompts)
	}
}

func TestEveryAgentReceivesOnlyTrustedSystemInstructions(t *testing.T) {
	for _, cli := range []taskflow.CodingAgent{taskflow.CodingAgentOpenCode, taskflow.CodingAgentCodex, taskflow.CodingAgentClaude} {
		task := taskflow.CreateTaskReq{CodingAgent: cli, Text: "本轮用户要求", PromptContext: taskflow.PromptContext{Version: taskflow.PromptContextVersion, AssistantInstructions: "管理员助手方法", ProjectRules: "项目业务要求", OutputRequirements: "交付偏好", UserInstructions: "用户传入的system_prompt"}}
		spec, err := projectSpec(Environment{ID: "task"}, task, "test-image")
		if err != nil {
			t.Fatal(err)
		}
		system := spec.Agents[0].SystemPrompt
		if strings.Count(system, taskflow.PlatformPrompt) != 1 || !strings.Contains(system, task.PromptContext.AssistantInstructions) {
			t.Fatalf("missing platform/assistant instructions for %v", cli)
		}
		for _, userContent := range []string{task.Text, task.PromptContext.ProjectRules, task.PromptContext.OutputRequirements, task.PromptContext.UserInstructions} {
			if strings.Contains(system, userContent) {
				t.Fatalf("%v elevated user content %q", cli, userContent)
			}
		}
	}
}
