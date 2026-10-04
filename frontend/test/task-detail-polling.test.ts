import assert from "node:assert/strict"
import test from "node:test"
import { loadTaskDetail, startTaskDetailPolling, taskDetailFailure, type TaskDetailResult } from "../src/utils/task-detail-polling.ts"
import { ConstsTaskStatus, type DomainProjectTask } from "../src/api/Api.ts"

const pending = { id: "task", status: ConstsTaskStatus.TaskStatusPending }
const processing = { id: "task", status: ConstsTaskStatus.TaskStatusProcessing }
const settle = async () => { for (let i = 0; i < 20; i++) await Promise.resolve() }

test("502 and an offline fetch recover to processing without reloading the page", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] })
  const responses = [
    Response.json({ code: 0, data: pending }),
    new Response("Bad Gateway", { status: 502 }),
    new TypeError("Failed to fetch"),
    Response.json({ code: 0, data: processing }),
  ]
  let calls = 0
  t.mock.method(globalThis, "fetch", async () => {
    calls++
    const response = responses.shift()
    if (response instanceof Error) throw response
    return response ?? Response.json({ code: 0, data: processing })
  })
  const tasks: DomainProjectTask[] = []
  let notices = 0
  const stop = startTaskDetailPolling({ loadTask: (signal) => loadTaskDetail("task", signal), onTask: task => tasks.push(task), onFailure: () => notices++ })
  try {
    await settle()
    assert.deepEqual(tasks, [pending])
    t.mock.timers.tick(2000); await settle()
    assert.equal(calls, 2)
    t.mock.timers.tick(2000); await settle()
    assert.equal(calls, 3)
    t.mock.timers.tick(3999); await settle()
    assert.equal(calls, 3)
    t.mock.timers.tick(1); await settle()
    assert.deepEqual(tasks, [pending, processing])
    assert.equal(notices, 1)
    t.mock.timers.tick(9999); await settle()
    assert.equal(calls, 4)
    t.mock.timers.tick(1); await settle()
    assert.equal(calls, 5)
  } finally { stop() }
})

test("transient errors back off to 30 seconds and reset after a successful read", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] })
  let calls = 0
  let result: TaskDetailResult = { kind: "retry" }
  const stop = startTaskDetailPolling({ loadTask: async () => { calls++; return result }, onTask: () => {} })
  try {
    await settle()
    for (const delay of [2000, 4000, 8000, 16000, 30000, 30000]) {
      const before = calls
      t.mock.timers.tick(delay - 1); await settle()
      assert.equal(calls, before)
      t.mock.timers.tick(1); await settle()
      assert.equal(calls, before + 1)
    }
    result = { kind: "success", task: pending }
    t.mock.timers.tick(30000); await settle()
    result = { kind: "retry" }
    t.mock.timers.tick(2000); await settle()
    const before = calls
    t.mock.timers.tick(2000); await settle()
    assert.equal(calls, before + 1)
  } finally { stop() }
})

test("permission, missing task and login errors stop; gateway, rate limit and database errors retry", async (t) => {
  for (const status of [400, 401, 403, 404, 410]) assert.equal(taskDetailFailure({ status }).kind, "stop")
  for (const code of [10001, 10002, 10603, 10605]) assert.equal(taskDetailFailure({ code }).kind, "stop")
  for (const status of [408, 425, 429, 500, 502, 503, 504]) assert.equal(taskDetailFailure({ status }).kind, "retry")
  for (const code of [10000, 10004, 10005, 10006]) assert.equal(taskDetailFailure({ code }).kind, "retry")
  t.mock.timers.enable({ apis: ["setTimeout"] })
  let calls = 0
  const stop = startTaskDetailPolling({ loadTask: async () => { calls++; return taskDetailFailure({ status: 404 }) }, onTask: () => assert.fail("No task expected") })
  await settle()
  t.mock.timers.tick(100000); await settle()
  assert.equal(calls, 1)
  stop()
})

test("navigation aborts the request and ignores a late response from the previous task", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] })
  let resolve!: (result: TaskDetailResult) => void
  let signal!: AbortSignal
  let calls = 0
  const stop = startTaskDetailPolling({ loadTask: async (nextSignal) => {
    calls++; signal = nextSignal
    return new Promise<TaskDetailResult>(done => { resolve = done })
  }, onTask: () => assert.fail("Stale task must not be applied") })
  stop()
  assert.equal(signal.aborted, true)
  resolve({ kind: "success", task: pending })
  await settle()
  t.mock.timers.tick(100000); await settle()
  assert.equal(calls, 1)
})

test("stopping during a retry delay cancels the next request", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] })
  let calls = 0
  const stop = startTaskDetailPolling({ loadTask: async () => { calls++; throw new TypeError("offline") }, onTask: () => {} })
  await settle()
  stop()
  t.mock.timers.tick(100000); await settle()
  assert.equal(calls, 1)
})
