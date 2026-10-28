import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test, { type TestContext } from "node:test";
import vm from "node:vm";
import ts from "typescript";
import { ConstsTaskStatus } from "../src/api/Api.ts";
import { mergeContextUsage } from "../src/components/console/task/context-usage.ts";
import { TaskMessageHandler } from "../src/components/console/task/task-message-handler.ts";
import { TaskStreamClient } from "../src/components/console/task/task-stream-client.ts";
import { b64encode } from "../src/utils/message-data.ts";

// Run the real page effects and callbacks without mounting its unrelated panels.
// Only HTTP/WebSocket boundaries and React state setters are replaced. In
// particular, history parsing and attach replay use the production message code.
const source = ts.createSourceFile("task-detail.tsx", readFileSync(
  new URL("../src/pages/console/user/task/task-detail.tsx", import.meta.url), "utf8",
), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);

function findNode(predicate: (node: ts.Node) => boolean): ts.Node {
  let found: ts.Node | undefined;
  function visit(node: ts.Node) {
    if (!found && predicate(node)) found = node;
    if (!found) ts.forEachChild(node, visit);
  }
  visit(source);
  assert.ok(found, "page callback/expression exists");
  return found;
}

function evaluate(expression: ts.Node, context: vm.Context) {
  const compiled = ts.transpileModule(`globalThis.pageExpression = (${expression.getText(source)});`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
  }).outputText;
  vm.runInContext(compiled, context);
  return context.pageExpression;
}

function pageVariable(name: string, context: vm.Context) {
  const node = findNode(node => ts.isVariableDeclaration(node)
    && ts.isIdentifier(node.name) && node.name.text === name) as ts.VariableDeclaration;
  assert.ok(node.initializer);
  const expression = ts.isCallExpression(node.initializer)
    && ["React.useCallback", "React.useMemo"].includes(node.initializer.expression.getText(source))
    ? node.initializer.arguments[0] : node.initializer;
  return evaluate(expression, context);
}

function pageEffect(marker: string, context: vm.Context): () => void {
  const node = findNode(node => ts.isCallExpression(node)
    && node.expression.getText(source) === "React.useEffect"
    && node.arguments[0]?.getText(source).includes(marker)) as ts.CallExpression;
  return evaluate(node.arguments[0], context);
}

type StreamChunk = { type: string; seq: number; timestamp: number; kind?: string; data?: string };

class Socket {
  static OPEN = 1;
  static instances: Socket[] = [];
  readyState = 0;
  onopen?: () => void;
  onmessage?: (event: { data: string }) => void;
  constructor(readonly url: string) { Socket.instances.push(this); }
  send() {}
  close() { this.readyState = 3; }
  open() { this.readyState = 1; this.onopen?.(); }
  receive(chunk: StreamChunk) { this.onmessage?.({ data: JSON.stringify(chunk) }); }
}

function turn(id: string, seq: number): StreamChunk[] {
  const timestamp = 1770000000000 + seq * 100;
  return [
    { type: "user-input", seq, timestamp,
      data: b64encode(JSON.stringify({ content: b64encode(`request ${id}`), client_message_id: id, attachments: [] })) },
    { type: "task-running", kind: "acp_event", seq: seq + 1, timestamp: timestamp + 1,
      data: b64encode(JSON.stringify({ update: { sessionUpdate: "agent_message_chunk", content: { type: "text", text: `answer ${id}` } } })) },
    { type: "task-ended", seq: seq + 2, timestamp: timestamp + 2 },
  ];
}

function fixture(t: TestContext, status: ConstsTaskStatus, work = true) {
  const originalSocket = globalThis.WebSocket;
  const originalLocation = globalThis.location;
  Object.assign(globalThis, { WebSocket: Socket, location: { protocol: "http:", host: "localhost" } });
  const requests: Array<{ id: string; cursor?: string; limit: number }> = [];
  const responses: Array<{ chunks: unknown[]; has_more: boolean; next_cursor?: string }> = [];
  const context = vm.createContext({
    taskId: "task-history-fixture", task: { status, type: work ? "work" : "coding" }, isWorkTask: work,
    ConstsTaskStatus, TaskStreamClient, TaskMessageHandler, mergeContextUsage,
    streamStatus: "inited", streamClientRef: { current: null }, cancelledRef: { current: false },
    historyLoaded: false, historyLoading: false, historyCursorReady: false,
    historyCursor: null, historyHasMore: true, historyCursorRef: { current: null },
    historyLoadingRef: { current: false }, historyLoadedRef: { current: false },
    historyAutoLoadAttemptedRef: { current: false }, rawLiveMessages: [], rawHistoryMessages: [],
    contextUsage: { size: null, used: null },
    t: (key: string) => key, toast: { error: (message: string) => assert.fail(message) }, notifyNavigation() {},
    apiRequest: async (method: string, params: { id: string; cursor?: string; limit: number }, _extra: unknown,
      callback: (response: unknown) => void) => {
      assert.equal(method, "v1UsersTasksRoundsList");
      requests.push({ ...params });
      callback({ code: 0, data: responses.shift() ?? { chunks: [], has_more: false } });
    },
  });
  const states = ["streamStatus", "rawLiveMessages", "rawHistoryMessages", "availableCommands", "plan", "contextUsage",
    "timeCost", "streamConnectionState", "streamCloseReason", "queuedReplyIds", "submittingReplyIds", "sending",
    "historyCursorReady", "historyCursor", "historyHasMore", "historyLoaded", "historyLoading"];
  for (const name of states) {
    context[`set${name[0].toUpperCase()}${name.slice(1)}`] = (next: unknown) => {
      context[name] = typeof next === "function" ? next(context[name]) : next;
    };
  }
  context.taskInteractive = pageVariable("taskInteractive", context);
  context.disconnectStreamClient = pageVariable("disconnectStreamClient", context);
  context.connectStreamClient = pageVariable("connectStreamClient", context);
  context.fetchTaskRounds = pageVariable("fetchTaskRounds", context);
  t.after(() => {
    context.disconnectStreamClient();
    Object.assign(globalThis, { WebSocket: originalSocket, location: originalLocation });
  });
  return {
    context, requests,
    initialEffects() {
      pageEffect('connectStreamClient("attach")', context)();
      pageEffect("if (historyAutoLoadAttemptedRef.current) return", context)();
    },
    replayLatest(cursor: string | null, hasMore: boolean) {
      const socket = Socket.instances.at(-1)!;
      socket.open();
      socket.receive({ type: "cursor", seq: 0, timestamp: 1770000000000,
        data: b64encode(JSON.stringify({ cursor, has_more: hasMore })) });
      turn("latest", 10).forEach(chunk => socket.receive(chunk));
    },
    respondWithTurn(id: string, nextCursor?: string) {
      responses.push({
        chunks: turn(id, id === "latest" ? 10 : 1).map(({ type, ...chunk }) => ({
          ...chunk, event: type, timestamp: chunk.timestamp * 1000000,
        })),
        has_more: !!nextCursor, next_cursor: nextCursor,
      });
    },
    loadHistory(cursor?: string, limit?: number): Promise<void> {
      return context.fetchTaskRounds(cursor, limit);
    },
    showHistoryButton(): boolean { return pageVariable("showHistoryLoadButton", context); },
    messages(): Array<{ type: string; data: { content?: string; clientMessageId?: string } }> {
      context.historyMessages = context.rawHistoryMessages;
      context.liveMessages = context.rawLiveMessages;
      context.canControl = true;
      context.handleReloadSession = () => {};
      context.handleSend = () => {};
      return Array.from(pageVariable("messages", context)());
    },
  };
}

for (const status of [ConstsTaskStatus.TaskStatusFinished, ConstsTaskStatus.TaskStatusError]) {
  test(`reopening ${status} work task attaches once without concurrently fetching its latest round`, async t => {
    const f = fixture(t, status);
    const before = Socket.instances.length;
    f.initialEffects();
    assert.equal(Socket.instances.length, before + 1);
    assert.match(Socket.instances.at(-1)!.url, /mode=attach/);
    assert.deepEqual(f.requests, [], "REST must not race the attach replay");
    assert.equal(f.showHistoryButton(), false, "cursor has not arrived yet");
    f.replayLatest("older-boundary", true);
    f.initialEffects();
    assert.equal(Socket.instances.length, before + 1);
    assert.deepEqual(f.requests, []);
    const messages = f.messages();
    assert.deepEqual(messages.filter(message => message.type === "user_input").map(message => message.data.clientMessageId), ["latest"]);
    assert.deepEqual(messages.filter(message => message.type === "agent_message_chunk").map(message => message.data.content), ["answer latest"]);
    assert.equal(f.showHistoryButton(), true);
  });
}

test("interactive history only requests earlier rounds with the attach cursor and appends each round once", async t => {
  const f = fixture(t, ConstsTaskStatus.TaskStatusFinished);
  f.initialEffects();
  await f.loadHistory();
  assert.deepEqual(f.requests, [], "an index jump before attach has no safe cursor");
  f.replayLatest("older-boundary", true);
  f.respondWithTurn("earlier");
  await f.loadHistory(f.context.historyCursor, 1);
  assert.deepEqual(f.requests, [{ id: "task-history-fixture", cursor: "older-boundary", limit: 1 }]);
  assert.deepEqual(f.messages().filter(message => message.type === "user_input").map(message => message.data.clientMessageId), ["earlier", "latest"]);
  assert.deepEqual(f.messages().filter(message => message.type === "agent_message_chunk").map(message => message.data.content), ["answer earlier", "answer latest"]);
  assert.equal(f.context.historyCursorRef.current, null);
  assert.equal(f.showHistoryButton(), false);
  await f.loadHistory(f.context.historyCursorRef.current ?? undefined, 1);
  assert.equal(f.requests.length, 1, "exhausted history cannot fall back to latest-round REST");
});

test("a single attach round with null cursor has no history button and cannot fetch the latest round again", async t => {
  const f = fixture(t, ConstsTaskStatus.TaskStatusFinished);
  f.initialEffects();
  f.replayLatest(null, false);
  assert.equal(f.context.historyCursorReady, true);
  assert.equal(f.context.historyLoaded, false, "attach does not mark REST history as loaded");
  assert.equal(f.showHistoryButton(), false);
  await f.loadHistory();
  assert.deepEqual(f.requests, []);
  assert.equal(f.messages().filter(message => message.type === "user_input").length, 1);
});

for (const status of [ConstsTaskStatus.TaskStatusFinished, ConstsTaskStatus.TaskStatusError]) {
  test(`noninteractive ${status} task still loads its initial history without an attach cursor`, async t => {
    const f = fixture(t, status, false);
    const before = Socket.instances.length;
    f.respondWithTurn("latest");
    f.initialEffects();
    assert.equal(Socket.instances.length, before, "a historical coding task does not attach");
    assert.deepEqual(f.requests, [{ id: "task-history-fixture", limit: 1 }]);
    assert.equal(f.context.historyLoaded, true);
    assert.deepEqual(f.messages().filter(message => message.type === "user_input").map(message => message.data.clientMessageId), ["latest"]);
    assert.equal(f.showHistoryButton(), false);
    await Promise.resolve();
    f.initialEffects();
    assert.equal(f.requests.length, 1);
  });
}
