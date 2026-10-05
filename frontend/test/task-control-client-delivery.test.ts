import assert from "node:assert/strict"
import test, { type TestContext } from "node:test"
import { TaskControlClient, type TaskControlCallResponse, type TaskControlClientOptions } from "../src/components/console/task/task-control-client.ts"
import { b64encode, b64decode } from "../src/utils/message-data.ts"

class Socket {
  static CONNECTING = 0
  static OPEN = 1
  static instances: Socket[] = []
  readyState = 0
  sent: string[] = []
  onopen?: () => void
  onclose?: (event: { code: number; reason?: string }) => void
  onmessage?: (event: { data: string }) => void
  onerror?: () => void
  constructor(public url: string) { Socket.instances.push(this) }
  send(data: string) { this.sent.push(data) }
  close() { this.readyState = 3 }
  open() { this.readyState = 1; this.onopen?.() }
  disconnect(code = 1006) { this.readyState = 3; this.onclose?.({ code }) }
  response(id: string, response: TaskControlCallResponse) {
    this.onmessage?.({ data: JSON.stringify({ type: "call-response", data: b64encode(JSON.stringify({ request_id: id, ...response })) }) })
  }
  request(index = 0) { return JSON.parse(b64decode(JSON.parse(this.sent[index]).data)) }
}
const settle = async () => { for (let i = 0; i < 10; i++) await Promise.resolve() }
function setup(t: TestContext, options: Partial<TaskControlClientOptions> = {}) {
  t.mock.timers.enable({ apis: ["setTimeout"] })
  const originals = { WebSocket: globalThis.WebSocket, location: globalThis.location, sessionStorage: globalThis.sessionStorage }
  const storage = new Map<string, string>()
  Object.assign(globalThis, { WebSocket: Socket, location: { protocol: "http:", host: "localhost" }, sessionStorage: {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => storage.set(key, value),
    removeItem: (key: string) => storage.delete(key),
  } })
  Socket.instances = []
  const clients: TaskControlClient[] = []
  const make = (overrides: Partial<TaskControlClientOptions> = {}) => {
    const client = new TaskControlClient({ taskId: "task", userId: "user", ...options, ...overrides })
    clients.push(client); client.connect()
    const socket = Socket.instances.at(-1)!; socket.open()
    return { client, socket }
  }
  t.after(() => { for (const client of clients) client.dispose(); Object.assign(globalThis, originals) })
  return { ...make(), storage, make }
}
for (const kind of ["model", "resources", "restart", "clear"] as const) {
  test(`${kind}: 15s deadline preserves ID/selection and consumes a 25s late terminal result once`, async t => {
    const results: TaskControlCallResponse[] = []
    const { client, socket, storage } = setup(t, { onOperationResult: (_, response) => results.push(response) })
    let completed = false
    const promise = kind === "model" ? client.switchModel("selected-model")
      : kind === "resources" ? client.switchAgentResources(["selected-skill"], ["selected-plugin"])
      : client.restartOperation(kind === "restart")
    void promise.then(() => { completed = true })
    const original = socket.request()
    t.mock.timers.tick(15000); await settle()
    assert.equal(completed, false)
    assert.equal(client.getState().operation?.status, "uncertain")
    assert.equal(socket.sent.length, 1)
    assert.equal(client.getState().operation?.requestId, original.request_id)
    assert.equal(storage.size, 1)
    t.mock.timers.tick(10000)
    socket.response(original.request_id, { success: true, status: "succeeded", session_id: "final-session" })
    assert.equal((await promise)?.success, true)
    socket.response(original.request_id, { success: true, status: "succeeded" })
    assert.equal(results.length, 1)
    assert.equal(client.getState().operation, null)
    assert.equal(storage.size, 0)
    t.mock.timers.tick(120000)
    assert.equal(socket.sent.length, 1)
  })
}

test("30s+ pending retries back off with original request until final", async t => {
  const { client, socket } = setup(t)
  let completed = false
  const promise = client.switchModel("original-model", false)
  void promise.then(() => { completed = true })
  const original = socket.request()
  t.mock.timers.tick(30000)
  socket.response(original.request_id, { success: false, status: "pending", error: "deadline exceeded" })
  await settle()
  assert.equal(completed, false)
  assert.equal(client.getState().operation?.status, "pending")
  for (const delay of [2000, 4000, 8000, 16000, 30000, 30000]) {
    const before = socket.sent.length
    t.mock.timers.tick(delay - 1); assert.equal(socket.sent.length, before)
    t.mock.timers.tick(1); assert.deepEqual(socket.request(before), original)
    socket.response(original.request_id, { success: false, status: "pending" })
  }
  socket.response(original.request_id, { success: true, status: "succeeded" })
  assert.equal((await promise)?.success, true)
})

test("a missing response does not pile retries into the server's 30s wait", async t => {
  const { client, socket } = setup(t)
  const promise = client.restartOperation(false)
  const original = socket.request()
  t.mock.timers.tick(35000); t.mock.timers.tick(1999)
  assert.equal(socket.sent.length, 1)
  t.mock.timers.tick(1)
  assert.equal(socket.sent.length, 2)
  assert.deepEqual(socket.request(1), original)
  socket.response(original.request_id, { success: false, status: "failed", error: "rejected" })
  assert.equal((await promise)?.status, "failed")
})

test("disconnect restores original ID after bounded reconnect and ignores stale socket replies", async t => {
  let callbacks = 0
  const { client, socket } = setup(t, { onOperationResult: () => callbacks++ })
  const promise = client.switchAgentResources(["skill"], [])
  const original = socket.request()
  socket.disconnect()
  assert.equal(client.getState().operation?.status, "uncertain")
  t.mock.timers.tick(999); assert.equal(Socket.instances.length, 1)
  t.mock.timers.tick(1)
  const restored = Socket.instances.at(-1)!; restored.open()
  assert.deepEqual(restored.request(), original)
  socket.response(original.request_id, { success: true }); assert.equal(callbacks, 0)
  restored.response(original.request_id, { success: true })
  assert.equal((await promise)?.status, "succeeded")
  assert.equal(callbacks, 1)
})

test("duplicate clicks share one request; other selections/operation kinds cannot replace it", async t => {
  const { client, socket } = setup(t)
  const first = client.switchModel("first")
  const duplicate = client.switchModel("first")
  assert.equal(socket.sent.length, 1)
  assert.equal((await client.switchModel("second"))?.error, "operation_in_progress")
  assert.equal((await client.restartOperation(false))?.error, "operation_in_progress")
  assert.equal(socket.sent.length, 1)
  assert.equal(client.getState().operation?.payload.model_id, "first")
  socket.response(socket.request().request_id, { success: true })
  assert.equal((await first)?.success, true)
  assert.equal((await duplicate)?.success, true)
})

test("explicit failure clears pending/storage; a later deliberate action gets a new ID", async t => {
  const { client, socket, storage } = setup(t)
  const promise = client.switchModel("model")
  const original = socket.request().request_id
  socket.response(original, { status: "failed", success: false, error: "permission denied" })
  assert.equal((await promise)?.error, "permission denied")
  assert.equal(client.getState().operation, null); assert.equal(storage.size, 0)
  void client.switchModel("model")
  assert.notEqual(socket.request(1).request_id, original)
})

test("navigation/refresh detaches callbacks and retries, then restores same task/account only", async t => {
  let oldCompletions = 0, restoredCompletions = 0
  const { client, socket, make, storage } = setup(t, { onOperationResult: () => oldCompletions++ })
  const promise = client.restartOperation(false)
  const original = socket.request()
  client.dispose()
  assert.equal((await promise)?.status, "detached")
  t.mock.timers.tick(120000)
  assert.equal(socket.sent.length, 1); assert.equal(Socket.instances.length, 1)
  socket.response(original.request_id, { success: true })
  assert.equal(oldCompletions, 0); assert.equal(storage.size, 1)
  assert.equal(make({ taskId: "another-task" }).socket.sent.length, 0)
  assert.equal(make({ userId: "another-account" }).socket.sent.length, 0)
  const resumed = make({ onOperationResult: () => restoredCompletions++ })
  assert.deepEqual(resumed.socket.request(), original)
  resumed.socket.response(original.request_id, { status: "succeeded", success: true })
  assert.equal(restoredCompletions, 1); assert.equal(oldCompletions, 0); assert.equal(storage.size, 0)
})

test("malformed, mismatched and unknown-operation saved requests are removed without sending", t => {
  const { client, storage, make } = setup(t); client.dispose()
  const key = "jingjiaagent:task-control-operation:user:task"
  const valid = { version: 1, taskId: "task", userId: "user", requestId: "original", kind: "restart", payload: { load_session: true } }
  for (const record of ["{", "null", JSON.stringify({ ...valid, taskId: "another" }), JSON.stringify({ ...valid, userId: "another" }), JSON.stringify({ ...valid, kind: "execute" }), JSON.stringify({ ...valid, payload: { load_session: "true" } })]) {
    storage.set(key, record)
    const next = make()
    assert.equal(next.socket.sent.length, 0); assert.equal(storage.size, 0); next.client.dispose()
  }
})

test("public payload is allowlisted; injected configuration cannot be persisted or replayed", t => {
  const { client, socket, storage } = setup(t)
  void client.call("restart", { load_session: true, api_key: "not-for-storage", business_mutation: { arbitrary: true }, request_id: "spoof" })
  const request = socket.request()
  assert.deepEqual(Object.keys(request).sort(), ["load_session", "request_id"])
  assert.notEqual(request.request_id, "spoof")
  assert.equal([...storage.values()].join().includes("not-for-storage"), false)
})

test("authoritative access loss clears recovery state and policy-denied connects stop", async t => {
  const { client, socket, storage } = setup(t)
  const promise = client.restartOperation(true); client.rejectOperation("Task access revoked")
  assert.equal((await promise)?.status, "failed"); assert.equal(storage.size, 0)
  const again = client.restartOperation(true); socket.disconnect(1008)
  assert.equal((await again)?.status, "failed")
  t.mock.timers.tick(100000); assert.equal(Socket.instances.length, 1)
})

test("read-only calls retain their old timeout/null contract", async t => {
  const { client } = setup(t)
  const read = client.getFileList("/"); t.mock.timers.tick(5000)
  assert.equal(await read, null); assert.equal(client.getState().operation, null)
})

test("malformed or inconsistent long-operation replies cannot clear the original request", async t => {
  const { client, socket, storage } = setup(t)
  const promise = client.restartOperation(true)
  const requestID = socket.request().request_id
  for (const response of [{}, { status: "succeeded" }, { status: "failed", success: true }] as TaskControlCallResponse[]) {
    socket.response(requestID, response)
    assert.equal(client.getState().operation?.requestId, requestID)
    assert.equal(storage.size, 1)
  }
  socket.response(requestID, { success: true, status: "succeeded" })
  assert.equal((await promise)?.success, true)
})

test("repeated disconnects back off to 30s rather than spinning reconnects", t => {
  const { socket } = setup(t)
  let active = socket
  for (const delay of [1000, 2000, 4000, 8000, 16000, 30000, 30000]) {
    active.disconnect()
    const before = Socket.instances.length
    t.mock.timers.tick(delay - 1); assert.equal(Socket.instances.length, before)
    t.mock.timers.tick(1); assert.equal(Socket.instances.length, before + 1)
    active = Socket.instances.at(-1)!; active.open()
  }
})

test("a late old-socket close cannot cancel the new socket's connect deadline", t => {
  const { socket } = setup(t)
  socket.disconnect(); t.mock.timers.tick(1000)
  const next = Socket.instances.at(-1)!
  assert.equal(next.readyState, Socket.CONNECTING)
  socket.disconnect()
  t.mock.timers.tick(10000)
  assert.equal(next.readyState, 3)
})
