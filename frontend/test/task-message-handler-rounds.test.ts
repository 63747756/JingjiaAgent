import assert from "node:assert/strict";
import test from "node:test";
import { TaskMessageHandler, type TaskMessageRawChunk } from "../src/components/console/task/task-message-handler.ts";
import { b64encode } from "../src/utils/message-data.ts";

const start = 1770000000000;
const encode = (value: unknown) => b64encode(JSON.stringify(value));
const input = (turn: number, timestamp = start) => ({
  event: "user-input", turn_seq: turn, timestamp,
  data: encode({ content: b64encode(`第 ${turn} 轮`), attachments: [] }),
});
const text = (turn: number, content: string, timestamp = start + 1000) => ({
  event: "task-running", kind: "acp_event", turn_seq: turn, timestamp,
  data: encode({ update: { sessionUpdate: "agent_message_chunk", content: { type: "text", text: content } } }),
});
const final = (turn: number, content: string) => ({
  event: "agent-final", turn_seq: turn, timestamp: start + 23000, data: encode({ content }),
});
const ended = (turn: number, status?: string, timestamp = start + 24000) => ({
  event: "task-ended", turn_seq: turn, timestamp,
  ...(status ? { data: encode({ status }) } : {}),
});
const plan = (turn: number, entries: { content: string; status: string }[], timestamp = start + 1000) => ({
  event: "task-running", kind: "acp_event", turn_seq: turn, timestamp,
  data: encode({ update: { sessionUpdate: "plan", entries } }),
});
const acp = (turn: number, update: Record<string, unknown>) => ({
  event: "task-running", kind: "acp_event", turn_seq: turn, timestamp: start + 1000,
  data: encode({ update }),
});

test("successful rounds preserve authoritative duration and final metadata without mutating earlier snapshots", () => {
  const handler = new TaskMessageHandler();
  const running = handler.pushChunks([input(3), text(3, "正在检查。最终结果")]);
  handler.pushChunk(final(3, "最终结果"));
  handler.pushChunk(final(3, "最终结果"));
  const finished = handler.pushChunk(ended(3, "complete"));
  assert.equal(finished.status, "finished");
  assert.equal(finished.messages.length, 3, "Repeated final markers must not duplicate the final answer");
  assert.deepEqual(finished.messages[0].round, {
    id: "turn-3", turnSeq: 3, status: "complete", startedAt: start, endedAt: start + 24000,
  });
  assert.ok(finished.messages.every(message => message.round === finished.messages[0].round));
  assert.equal(finished.messages[1].data.content, "正在检查。最终结果", "Presentation strips the compatibility text using the final marker");
  assert.equal(finished.messages[2].isFinal, true);
  assert.equal(finished.messages[2].data.content, "最终结果");
  assert.equal(running.messages[0].round?.status, "running");
  assert.equal(running.messages[0].round?.endedAt, undefined);
});

test("empty final replies leave process text unmarked", () => {
  for (const content of ["", " \n\t"]) {
    const handler = new TaskMessageHandler();
    const running = handler.pushChunks([input(3), text(3, "正在检查")]);
    const finalState = handler.pushChunk(final(3, content));
    assert.equal(finalState.messages.length, 2);
    assert.equal(finalState.messages.some(message => message.isFinal), false);
    assert.deepEqual(finalState.messages, running.messages);
    const finished = handler.pushChunk(ended(3, "complete"));
    assert.equal(finished.messages[0].round?.status, "complete");
    assert.equal(finished.messages.some(message => message.isFinal), false);
    assert.equal(finished.messages[1].data.content, "正在检查");
    assert.equal(running.messages[0].round?.status, "running", "Previous snapshots remain immutable");
  }
});

test("invalid final payloads do not claim an authoritative final reply", () => {
  const result = new TaskMessageHandler().pushChunks([input(3), {
    ...final(3, ""), data: encode({ content: null }),
  }]);
  assert.equal(result.messages.length, 1);
  assert.equal(result.messages.some(message => message.isFinal), false);
});

test("REST nanosecond replay and WebSocket millisecond replay produce matching round metadata", () => {
  const chunks = [input(4), text(4, "结果"), final(4, "结果"), ended(4, "complete")];
  const live = new TaskMessageHandler().pushChunks(chunks);
  const history = new TaskMessageHandler().pushChunks(chunks.map(chunk => ({ ...chunk, timestamp: chunk.timestamp * 1e6 })));
  assert.deepEqual(history.messages.map(message => message.round), live.messages.map(message => message.round));
  assert.equal(history.messages[0].id, live.messages[0].id);
  assert.equal(history.messages.at(-1)?.id, live.messages.at(-1)?.id);
});

test("failed, canceled, and unlabelled terminals cannot become successful rounds", () => {
  for (const [status, expected] of [["failed", "failed"], ["canceled", "canceled"], [undefined, "unknown"]] as const) {
    const result = new TaskMessageHandler().pushChunks([input(2), text(2, "部分输出"), ended(2, status)]);
    assert.equal(result.messages[0].round?.status, expected);
    assert.equal(result.messages[0].round?.endedAt, start + 24000);
    assert.equal(result.status, "finished");
  }
});

test("legacy terminal payloads without JSON still close the round conservatively", () => {
  const handler = new TaskMessageHandler();
  const result = handler.pushChunks([input(0), text(0, "输出"), {
    ...ended(0), data: b64encode(""),
  }]);
  assert.equal(result.status, "finished");
  assert.equal(result.messages[0].round?.status, "unknown");
});

test("history cleanup and transport errors never synthesize a successful completion", () => {
  const handler = new TaskMessageHandler();
  handler.pushChunks([input(1), text(1, "执行中")]);
  const cleaned = handler.finalizeCycle();
  assert.equal(cleaned.messages[0].round?.status, "running");
  assert.equal(cleaned.messages[0].round?.endedAt, undefined);
  const disconnected = handler.setError();
  assert.equal(disconnected.messages[0].round?.status, "running");
  assert.equal(disconnected.messages[0].round?.endedAt, undefined);
});

test("turn boundaries prevent adjacent assistant chunks from merging across turns", () => {
  const handler = new TaskMessageHandler();
  const result = handler.pushChunks([
    input(5), text(5, "第一轮"), ended(5, "complete"),
    text(6, "第二轮", start + 25000),
  ]);
  assert.equal(result.messages.length, 3);
  assert.equal(result.messages[1].data.content, "第一轮");
  assert.equal(result.messages[1].round?.status, "complete");
  assert.equal(result.messages[2].data.content, "第二轮");
  assert.equal(result.messages[2].round?.id, "turn-6");
  assert.equal(result.messages[2].round?.status, "running");
});

test("all process message creation and replacement paths preserve their round", () => {
  const handler = new TaskMessageHandler();
  const questions = [{ question: "选择环境", options: [{ label: "测试" }] }];
  const result = handler.pushChunks([
    input(7),
    acp(7, { sessionUpdate: "agent_thought_chunk", content: { type: "text", text: "分析" } }),
    acp(7, { sessionUpdate: "agent_thought_chunk", content: { type: "text", text: "中" } }),
    text(7, "执行中"),
    acp(7, { sessionUpdate: "tool_call", toolCallId: "shell", kind: "execute", title: "检查", status: "pending" }),
    acp(7, { sessionUpdate: "tool_call_update", toolCallId: "shell", status: "completed", rawOutput: "OK" }),
    acp(7, { sessionUpdate: "tool_call", toolCallId: "converted", kind: "other", title: "准备问题", status: "pending" }),
    acp(7, { sessionUpdate: "tool_call_update", toolCallId: "converted", kind: "ask-user-question", rawInput: { questions } }),
    { event: "task-running", kind: "acp_ask_user_question", turn_seq: 7, timestamp: start + 2000,
      data: encode({ toolCall: { toolCallId: "direct-question", rawInput: { questions } } }) },
    { event: "reply-question", turn_seq: 7, timestamp: start + 3000,
      data: encode({ request_id: "direct-question", answers_json: JSON.stringify({ "选择环境": "测试" }) }) },
    acp(7, { sessionUpdate: "llm_call_retry", attempt: 1, message: "稍后重试" }),
    acp(7, { sessionUpdate: "compact_status", status: "started" }),
    acp(7, { sessionUpdate: "compact_status", status: "ended" }),
    plan(7, [{ content: "检查", status: "completed" }]),
    { event: "task-error", turn_seq: 7, timestamp: start + 4000, data: encode({ message: "失败原因" }) },
    { event: "user-cancel", turn_seq: 7, timestamp: start + 5000 },
    ended(7, "canceled"),
  ]);
  assert.ok(result.messages.every(message => message.round === result.messages[0].round));
  assert.equal(result.messages[0].round?.id, "turn-7");
  assert.equal(result.messages[0].round?.status, "canceled");
  assert.deepEqual(result.messages.map(message => message.type), [
    "user_input", "agent_thought_chunk", "agent_message_chunk", "tool_call", "ask_user_question",
    "ask_user_question", "alert_message", "alert_message", "alert_message", "plan", "error_message", "system_message",
  ]);
  assert.equal(result.messages[1].data.content, "分析中");
  assert.equal(result.messages[3].data.rawOutput, "OK");
  assert.equal(result.messages[4].data.status, "expired");
  assert.equal(result.messages[5].data.status, "completed");
  assert.equal(result.messages[5].data.questions?.[0].answer, "测试");
});

test("tool and question updates retain independent round metadata across turns", () => {
  const handler = new TaskMessageHandler();
  const roundEvents = (turn: number, answer: string) => [
    input(turn, start + turn * 30000),
    acp(turn, { sessionUpdate: "tool_call", toolCallId: `shell-${turn}`, title: "检查", status: "pending" }),
    acp(turn, { sessionUpdate: "tool_call_update", toolCallId: `shell-${turn}`, status: "completed", rawOutput: answer }),
    acp(turn, { sessionUpdate: "tool_call", toolCallId: `question-${turn}`, title: "question", status: "pending",
      rawInput: { questions: [{ question: "环境", options: [] }] } }),
    { event: "reply-question", turn_seq: turn, timestamp: start + turn * 30000 + 1000,
      data: encode({ request_id: `question-${turn}`, answers_json: JSON.stringify({ "环境": answer }) }) },
    ended(turn, "complete", start + turn * 30000 + 2000),
  ];
  const first = handler.pushChunks(roundEvents(1, "测试"));
  const result = handler.pushChunks(roundEvents(2, "生产"));
  const toolCalls = result.messages.filter(message => message.type === "tool_call");
  const questions = result.messages.filter(message => message.type === "ask_user_question");
  assert.deepEqual(toolCalls.map(message => [message.round?.id, message.data.rawOutput]), [["turn-1", "测试"], ["turn-2", "生产"]]);
  assert.deepEqual(questions.map(message => [message.round?.id, message.data.questions?.[0].answer]), [["turn-1", "测试"], ["turn-2", "生产"]]);
  assert.deepEqual(result.messages.slice(0, first.messages.length), first.messages);
});

test("optimistic input is assigned to its confirmed round without changing the previous round", () => {
  const handler = new TaskMessageHandler();
  const previous = handler.pushChunks([input(1), text(1, "上一轮"), ended(1, "complete")]);
  const pending = handler.applyOptimisticUserInput({ client_message_id: "second-input", content: "下一轮", attachments: [] });
  assert.equal(pending.messages.at(-1)?.round, undefined);
  const confirmation = {
    event: "user-input", turn_seq: 2, timestamp: start + 30000,
    data: encode({ client_message_id: "second-input", content: b64encode("下一轮"), attachments: [] }),
  };
  const confirmed = handler.pushChunks([confirmation, confirmation]);
  assert.equal(confirmed.messages.length, previous.messages.length + 1);
  assert.equal(confirmed.messages.at(-1)?.round?.id, "turn-2");
  assert.equal(confirmed.messages.at(-1)?.data.deliveryState, "confirmed");
  assert.deepEqual(confirmed.messages.slice(0, previous.messages.length), previous.messages);
  assert.equal(pending.messages.at(-1)?.data.deliveryState, "sending");
});

test("transport error messages keep the active round without completing it", () => {
  const result = new TaskMessageHandler().pushChunks([input(9), {
    event: "error", turn_seq: 9, timestamp: start + 1000, data: encode("连接中断"),
  }]);
  assert.equal(result.messages.at(-1)?.type, "error_message");
  assert.equal(result.messages.at(-1)?.round, result.messages[0].round);
  assert.equal(result.messages.at(-1)?.round?.status, "running");
  assert.equal(result.status, "error");
});

test("unsequenced initial and legacy rounds retain their own timestamps and terminal status", () => {
  const handler = new TaskMessageHandler();
  const result = handler.pushChunks([
    input(0), text(0, "第一轮"), ended(0, "complete"),
    input(0, start + 30000), text(0, "第二轮", start + 31000), ended(0, "canceled", start + 35000),
  ]);
  assert.notEqual(result.messages[0].round?.id, result.messages[2].round?.id);
  assert.equal(result.messages[0].round?.status, "complete");
  assert.equal(result.messages[2].round?.status, "canceled");
  assert.equal(result.messages[2].round?.startedAt, start + 30000);
  assert.equal(result.messages[2].round?.endedAt, start + 35000);
});

test("a final marker cannot absorb later compatibility streaming text", () => {
  const result = new TaskMessageHandler().pushChunks([input(8), final(8, "最终结果"), text(8, "最终结果")]);
  assert.equal(result.messages[1].data.content, "最终结果");
  assert.equal(result.messages[1].isFinal, true);
  assert.equal(result.messages[2].data.content, "最终结果");
  assert.equal(result.messages[2].isFinal, undefined);
});

test("backward history pages replay multiple turns oldest first with terminal metadata kept per turn", () => {
  const page = [
    { ...input(3, start + 60000), seq: 30 },
    { ...text(3, "第三轮", start + 61000), seq: 31 },
    { ...ended(3, "canceled", start + 65000), seq: 32 },
    { ...input(2, start + 30000), seq: 20 },
    { ...text(2, "第二轮", start + 31000), seq: 21 },
    { ...ended(2, "failed", start + 35000), seq: 22 },
    { ...input(1), seq: 10 },
    { ...text(1, "第一轮"), seq: 11 },
    { ...ended(1, "complete"), seq: 12 },
  ];
  const originalPageOrder = page.map(chunk => chunk.seq);
  const result = new TaskMessageHandler().pushChunks(page);
  const inputs = result.messages.filter(message => message.type === "user_input");
  assert.deepEqual(inputs.map(message => message.round?.id), ["turn-1", "turn-2", "turn-3"]);
  assert.deepEqual(inputs.map(message => message.round?.status), ["complete", "failed", "canceled"]);
  assert.deepEqual(inputs.map(message => message.round?.endedAt), [start + 24000, start + 35000, start + 65000]);
  assert.deepEqual(inputs.map(message => message.id), [
    `user-input-${start * 1e6}`,
    `user-input-${(start + 30000) * 1e6}`,
    `user-input-${(start + 60000) * 1e6}`,
  ]);
  assert.deepEqual(page.map(chunk => chunk.seq), originalPageOrder, "Replay must not mutate the API response array");
});

test("persistent sequence order repairs shuffled chunks and preserves equal-sequence order", () => {
  const result = new TaskMessageHandler().pushChunks([
    { ...ended(4, "complete"), seq: "18446744073709551615" },
    { ...text(4, "一"), seq: "18446744073709551613" },
    { ...input(4), seq: "18446744073709551612" },
    { ...text(4, "二"), seq: "18446744073709551613" },
  ]);
  assert.equal(result.messages[0].type, "user_input");
  assert.equal(result.messages[1].data.content, "一二");
  assert.equal(result.messages[0].round?.status, "complete");
  assert.equal(result.messages[0].round?.endedAt, start + 24000);
});

test("mixed or legacy history pages preserve caller ordering when persistent fields are missing", () => {
  for (const missingField of ["seq", "turn_seq"] as const) {
    const newer = { ...input(2, start + 30000), seq: 20 };
    const older: TaskMessageRawChunk = { ...input(1), seq: 10 };
    delete older[missingField];
    const result = new TaskMessageHandler().pushChunks([newer, older]);
    assert.deepEqual(result.messages.map(message => message.data.content), ["第 2 轮", "第 1 轮"]);
  }
});

test("single live chunks remain in arrival order instead of being reordered by sequence", () => {
  const handler = new TaskMessageHandler();
  handler.pushChunk({ ...input(2, start + 30000), seq: 20 });
  handler.pushChunk({ ...input(1), seq: 10 });
  assert.deepEqual(handler.getMessages().map(message => message.round?.id), ["turn-2", "turn-1"]);
});

test("plan updates retain one immutable latest snapshot in their own round", () => {
  const handler = new TaskMessageHandler();
  handler.pushChunk(input(1));
  const first = handler.pushChunk(plan(1, [{ content: "检查代码", status: "in_progress" }]));
  const firstPlan = first.messages.find(message => message.type === "plan")!;
  assert.equal(firstPlan.id, "plan-turn-1");
  assert.equal(firstPlan.round?.status, "running");
  const updated = handler.pushChunk(plan(1, [{ content: "检查代码", status: "completed" }], start + 2000));
  assert.equal(updated.messages.filter(message => message.type === "plan").length, 1);
  assert.equal(updated.messages[1].id, firstPlan.id);
  assert.equal(updated.messages[1].data.entries?.[0].status, "completed");
  assert.equal(firstPlan.data.entries?.[0].status, "in_progress");
  const finished = handler.pushChunk(ended(1, "complete"));
  assert.equal(finished.messages[1].round?.status, "complete");
  assert.equal(firstPlan.round?.status, "running");

  handler.pushChunk(input(2, start + 30000));
  const nextRound = handler.pushChunk(plan(2, [{ content: "修改代码", status: "in_progress" }], start + 31000));
  const plans = nextRound.messages.filter(message => message.type === "plan");
  assert.deepEqual(plans.map(message => message.id), ["plan-turn-1", "plan-turn-2"]);
  assert.deepEqual(plans.map(message => message.round?.status), ["complete", "running"]);
  assert.equal(plans[0].data.entries?.[0].content, "检查代码");
  assert.equal(plans[1].data.entries?.[0].content, "修改代码");
});

test("empty plan updates clear the saved snapshot as well as the running dock", () => {
  const handler = new TaskMessageHandler();
  handler.pushChunks([input(2), plan(2, [{ content: "旧计划", status: "in_progress" }])]);
  const cleared = handler.pushChunk(plan(2, [], start + 2000));
  assert.deepEqual(cleared.plan.entries, []);
  assert.equal(cleared.plan.version, 2);
  const plans = cleared.messages.filter(message => message.type === "plan");
  assert.equal(plans.length, 1);
  assert.deepEqual(plans[0].data.entries, []);
  const finished = handler.pushChunk(ended(2, "complete"));
  assert.deepEqual(finished.messages[1].data.entries, []);
  assert.equal(finished.messages[1].round?.status, "complete");
});
