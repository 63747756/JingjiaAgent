import assert from "node:assert/strict";
import test from "node:test";
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
