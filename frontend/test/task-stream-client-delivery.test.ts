import assert from "node:assert/strict";
import test, { type TestContext } from "node:test";
import { TaskStreamClient } from "../src/components/console/task/task-stream-client.ts";
import { TaskMessageHandler } from "../src/components/console/task/task-message-handler.ts";
import { b64encode } from "../src/utils/message-data.ts";

class Socket {
  static OPEN = 1;
  static instances: Socket[] = [];
  readyState = 0;
  sent: string[] = [];
  onopen?: () => void;
  onmessage?: (event: { data: string }) => void;
  onclose?: (event: object) => void;
  url: string;
  constructor(url: string) { this.url = url; Socket.instances.push(this); }
  send(data: string) { this.sent.push(data); }
  close() { this.readyState = 3; }
  open() { this.readyState = 1; this.onopen?.(); }
  receive(chunk: unknown) { this.onmessage?.({ data: JSON.stringify(chunk) }); }
}

const userChunk = (id: string, content = "中文消息", seq = 1) => ({
  type: "user-input", seq, timestamp: 1770000000000,
  data: b64encode(JSON.stringify({ content: b64encode(content), attachments: [], client_message_id: id })),
});

test("message appears before socket opens, authoritative echo confirms it once", () => {
  const oldSocket = globalThis.WebSocket;
  const oldLocation = globalThis.location;
  Object.assign(globalThis, { WebSocket: Socket, location: { protocol: "http:", host: "localhost" } });
  const client = TaskStreamClient.new({ taskId: "task", userInput: "中文消息" });
  try {
    client.connect();
    let state = client.getState();
    assert.equal(state.messages.length, 1);
    assert.equal(state.messages[0].data.deliveryState, "sending");
    const socket = Socket.instances.at(-1)!;
    assert.equal(socket.sent.length, 0);
    socket.open();
    const input = JSON.parse(atob(JSON.parse(socket.sent[0]).data));
    assert.match(input.client_message_id, /^[0-9a-f-]{36}$/);
    socket.receive(userChunk(input.client_message_id));
    socket.receive(userChunk(input.client_message_id));
    state = client.getState();
    assert.equal(state.messages.length, 1);
    assert.equal(state.messages[0].data.deliveryState, "confirmed");
    assert.equal(state.messages[0].data.content, "中文消息");
    client.disconnect();
  } finally { Object.assign(globalThis, { WebSocket: oldSocket, location: oldLocation }); }
});

test("a same-content older turn cannot confirm a new message with another ID", () => {
  const handler = new TaskMessageHandler();
  handler.applyOptimisticUserInput({ content: "中文消息", attachments: [], client_message_id: "new-id" });
  handler.pushChunk({ ...userChunk("old-id"), event: "user-input" });
  assert.equal(handler.getMessages().length, 2);
  assert.equal(handler.getMessages()[0].data.deliveryState, "sending");
  handler.setInputDeliveryState("uncertain");
  handler.pushChunk({ ...userChunk("new-id", "中文消息", 2), event: "user-input", timestamp: 1770000001000 });
  assert.equal(handler.getMessages().filter(message => message.data.clientMessageId === "new-id").length, 1);
  assert.equal(handler.getMessages()[0].data.deliveryState, "confirmed");
});

test("server rejection leaves a failed message while transport uncertainty stays recoverable", () => {
  const handler = new TaskMessageHandler();
  handler.applyOptimisticUserInput({ content: "中文消息", attachments: [], client_message_id: "new-id" });
  handler.setInputDeliveryState("uncertain");
  assert.equal(handler.getMessages()[0].data.deliveryState, "uncertain");
  handler.pushChunk({ event: "error", data: b64encode(JSON.stringify("request rejected")) });
  assert.equal(handler.getState().status, "error");
  assert.equal(handler.getMessages()[0].data.deliveryState, "failed");
  handler.setInputDeliveryState("uncertain");
  assert.equal(handler.getMessages()[0].data.deliveryState, "failed");
});

test("incremental text creates new message objects and leaves old snapshots unchanged", () => {
  const handler = new TaskMessageHandler();
  const chunk = (text: string) => ({ event: "task-running", kind: "acp_event",
    data: b64encode(JSON.stringify({ update: { sessionUpdate: "agent_message_chunk", content: { type: "text", text } } })) });
  const first = handler.pushChunk(chunk("中文"));
  const second = handler.pushChunk(chunk("回答"));
  assert.equal(first.messages[0].data.content, "中文");
  assert.equal(second.messages[0].data.content, "中文回答");
  assert.notEqual(first.messages[0], second.messages[0]);
});

const questionChunk = (requestId: string, seq = 1) => ({
  type: "task-running", kind: "acp_ask_user_question", seq, timestamp: 1770000000000 + seq,
  data: b64encode(JSON.stringify({ toolCall: { toolCallId: requestId, title: "question",
    rawInput: { questions: [{ question: "选择", options: [{ label: "同意" }] }] } } })),
});
const replyChunk = (requestId: string, seq = 3, cancelled = false) => ({
  type: "reply-question", seq, timestamp: 1770000000000 + seq,
  data: b64encode(JSON.stringify({ request_id: requestId, answers_json: JSON.stringify({ "选择": "同意" }), cancelled })),
});
const endedChunk = { type: "task-ended", seq: 2, timestamp: 1770000000002 };

function mockSocket(t: TestContext) {
  t.mock.timers.enable({ apis: ["setTimeout", "setInterval"] });
  t.mock.property(globalThis, "WebSocket", Socket);
  const oldLocation = globalThis.location;
  Object.assign(globalThis, { location: { protocol: "http:", host: "localhost" } });
  t.after(() => { Object.assign(globalThis, { location: oldLocation }); });
}

test("reconnect consumes a delayed base64 receipt before the reordered terminal without duplicate approval", (t) => {
  mockSocket(t);
  const client = TaskStreamClient.attach({ taskId: "task" });
  client.connect();
  const socket = Socket.instances.at(-1)!;
  socket.open();
  socket.receive(questionChunk("question"));
  assert.equal(client.sendReplyQuestion("question", { "选择": "同意" }), "sent");
  assert.equal(client.sendReplyQuestion("question", { "选择": "同意" }), "sent");
  assert.equal(socket.sent.length, 1);
  assert.deepEqual(client.getState().submittingReplyIds, ["question"]);
  socket.readyState = 3;
  socket.onclose?.({});
  assert.equal(client.getState().connectionState, "reconnecting");
  t.mock.timers.tick(500);
  const restored = Socket.instances.at(-1)!;
  restored.open();
  assert.equal(restored.sent.length, 0, "Already submitted approvals must not be resent");
  restored.receive(questionChunk("question"));
  restored.receive(replyChunk("question"));
  restored.receive(replyChunk("question"));
  assert.deepEqual(client.getState().submittingReplyIds, []);
  assert.equal(client.getState().messages.length, 1);
  assert.equal(client.getState().messages[0].data.status, "completed");
  assert.equal(client.getState().messages[0].data.questions?.[0].answer, "同意");
  restored.receive(endedChunk); // Historical terminal seq precedes the late receipt seq.
  const state = client.getState();
  assert.equal(state.status, "finished");
  assert.equal(state.connectionState, "closed");
  assert.equal(state.closeReason, "task_ended");
  assert.equal(state.messages[0].data.status, "completed");
  assert.equal(client.sendReplyQuestion("question", { "选择": "同意" }), "rejected");
  restored.onclose?.({});
  const count = Socket.instances.length;
  t.mock.timers.tick(120000);
  assert.equal(Socket.instances.length, count);
});

test("queued replies send once after reconnect and terminal expiry never confirms an optimistic answer", (t) => {
  mockSocket(t);
  const client = TaskStreamClient.attach({ taskId: "task" });
  client.connect();
  const socket = Socket.instances.at(-1)!;
  socket.open();
  socket.receive(questionChunk("question"));
  socket.readyState = 3;
  socket.onclose?.({});
  assert.equal(client.sendReplyQuestion("question", { "选择": "同意" }), "queued");
  assert.equal(client.sendReplyQuestion("question", { "选择": "同意" }), "queued");
  t.mock.timers.tick(500);
  const restored = Socket.instances.at(-1)!;
  restored.open();
  assert.equal(restored.sent.length, 1);
  assert.deepEqual(client.getState().queuedReplyIds, []);
  assert.deepEqual(client.getState().submittingReplyIds, ["question"]);
  restored.receive(endedChunk);
  assert.equal(client.getState().messages[0].data.status, "expired");
  assert.deepEqual(client.getState().submittingReplyIds, []);
  assert.equal(client.sendReplyQuestion("question", { "选择": "同意" }), "rejected");
});

test("manual disconnect while offline clears queued approvals and prevents reconnection", (t) => {
  mockSocket(t);
  const client = TaskStreamClient.attach({ taskId: "task" });
  client.connect();
  const socket = Socket.instances.at(-1)!;
  socket.open();
  socket.receive(questionChunk("question"));
  socket.readyState = 3;
  socket.onclose?.({});
  client.sendReplyQuestion("question", { "选择": "同意" });
  client.disconnect();
  const count = Socket.instances.length;
  t.mock.timers.tick(120000);
  assert.equal(Socket.instances.length, count);
  assert.equal(client.getState().connectionState, "closed");
  assert.deepEqual(client.getState().queuedReplyIds, []);
  assert.equal(client.sendReplyQuestion("question", { "选择": "同意" }), "rejected");
});

test("late callbacks from a replaced socket cannot close the active connection", (t) => {
  mockSocket(t);
  const client = TaskStreamClient.attach({ taskId: "task" });
  client.connect();
  const old = Socket.instances.at(-1)!;
  old.open();
  client.connect();
  const current = Socket.instances.at(-1)!;
  current.open();
  old.onclose?.({});
  old.receive(endedChunk);
  assert.equal(client.getState().connectionState, "connected");
  assert.equal(client.getState().status, "connected");
  client.disconnect();
});

test("historical late receipts correct expired questions without reopening a finished run", () => {
  const handler = new TaskMessageHandler();
  handler.pushChunks([questionChunk("question"), endedChunk, replyChunk("question")]
    .map(chunk => ({ ...chunk, event: chunk.type })));
  assert.equal(handler.getState().status, "finished");
  assert.equal(handler.getMessages()[0].data.status, "completed");
  assert.equal(handler.getMessages()[0].data.questions?.[0].answer, "同意");
  handler.pushChunk({ ...replyChunk("question", 4, true), event: "reply-question" });
  assert.equal(handler.getMessages()[0].data.status, "expired");
});
