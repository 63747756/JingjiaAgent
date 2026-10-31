import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import test from "node:test"
import vm from "node:vm"
import ts from "typescript"
import { createWorkContinuationPolling } from "../src/components/console/work/work-task-status-polling.ts"
import type { ContinuationStatus } from "../src/components/console/work/work-task-status-state.ts"

const settle = async () => { for (let i = 0; i < 20; i++) await Promise.resolve() }
const continuation = (overrides: Partial<ContinuationStatus> = {}): ContinuationStatus => ({
  id: "command", message_id: "current", state: "pending", phase: "starting", ...overrides,
})

test("only a newly confirmed input refreshes immediately, not optimistic input or replay", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] })
  let calls = 0
  const poller = createWorkContinuationPolling({ load: async () => { calls++; return [] }, receive: () => {} })
  try {
    poller.setInput(undefined, false)
    await settle()
    assert.equal(calls, 1, "initial page status is loaded")
    poller.setInput("current", false)
    await settle()
    assert.equal(calls, 1, "optimistic submission is not a receipt")
    t.mock.timers.tick(100)
    poller.setInput("current", true)
    await settle()
    assert.equal(calls, 2)
    poller.setInput("current", true)
    poller.setInput("current", false)
    poller.setInput("current", true)
    await settle()
    assert.equal(calls, 2, "a repeated receipt does not refresh again")
    t.mock.timers.tick(1999); await settle()
    assert.equal(calls, 2)
    t.mock.timers.tick(1); await settle()
    assert.equal(calls, 3)
  } finally { poller.stop() }
})

test("only the confirmed current starting phase uses one second; all other states use two", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] })
  const cases: { items: ContinuationStatus[]; confirmed?: boolean; delay: number }[] = [
    { items: [continuation()], delay: 1000 },
    { items: [continuation({ state: "submitting" })], delay: 1000 },
    ...["running", "unknown", "blocked", "waiting", "failed", "complete"].map(state => ({ items: [continuation({ state })], delay: 2000 })),
    ...["", "queued", "saving", "restoring", "waking", "preparing"].map(phase => ({ items: [continuation({ phase })], delay: 2000 })),
    { items: [continuation()], confirmed: false, delay: 2000 },
    { items: [continuation({ message_id: "previous" })], delay: 2000 },
    { items: [], delay: 2000 },
  ]
  for (const { items, confirmed = true, delay } of cases) {
    let calls = 0
    const poller = createWorkContinuationPolling({ load: async () => { calls++; return items }, receive: () => {} })
    try {
      poller.setInput("current", confirmed)
      await settle()
      t.mock.timers.tick(delay - 1); await settle()
      assert.equal(calls, 1, JSON.stringify({ items, confirmed }))
      t.mock.timers.tick(1); await settle()
      assert.equal(calls, 2, JSON.stringify({ items, confirmed }))
    } finally { poller.stop() }
  }
})

test("running and failed status requests leave fast polling without losing known status", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] })
  let calls = 0
  const received: ContinuationStatus[][] = []
  const poller = createWorkContinuationPolling({
    load: async () => {
      calls++
      if (calls === 2) throw new TypeError("offline")
      return [continuation({ state: calls >= 3 ? "running" : "pending" })]
    },
    receive: items => received.push(items),
  })
  try {
    poller.setInput("current", true)
    await settle()
    t.mock.timers.tick(1000); await settle()
    assert.equal(calls, 2)
    assert.equal(received.length, 1)
    t.mock.timers.tick(1999); await settle()
    assert.equal(calls, 2)
    t.mock.timers.tick(1); await settle()
    assert.equal(received[1][0].state, "running")
    t.mock.timers.tick(1999); await settle()
    assert.equal(calls, 3)
    t.mock.timers.tick(1); await settle()
    assert.equal(calls, 4)
  } finally { poller.stop() }
})

test("a new receipt aborts the older request and waits for it before one fresh request", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] })
  const requests: { signal: AbortSignal; resolve: (items: ContinuationStatus[]) => void }[] = []
  const received: ContinuationStatus[][] = []
  const poller = createWorkContinuationPolling({
    load: signal => new Promise(resolve => { requests.push({ signal, resolve }) }),
    receive: items => received.push(items),
  })
  try {
    poller.setInput("previous", true)
    poller.setInput("current", false)
    assert.equal(requests[0].signal.aborted, true)
    poller.setInput("current", true)
    poller.refresh()
    poller.refresh()
    t.mock.timers.tick(10000); await settle()
    assert.equal(requests.length, 1, "slow or abort-ignoring requests cannot overlap")
    requests[0].resolve([continuation({ message_id: "previous", state: "running" })])
    await settle()
    assert.equal(received.length, 0, "a previous round cannot publish its late result")
    assert.equal(requests.length, 2, "refresh requests are coalesced")
    requests[1].resolve([continuation()])
    await settle()
    assert.equal(received[0][0].message_id, "current")
    t.mock.timers.tick(999); await settle()
    assert.equal(requests.length, 2)
    t.mock.timers.tick(1); await settle()
    assert.equal(requests.length, 3)
  } finally { poller.stop() }
})

test("switching to an unconfirmed round cancels the prior timer without immediate polling", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] })
  let calls = 0
  const poller = createWorkContinuationPolling({ load: async () => { calls++; return [continuation()] }, receive: () => {} })
  try {
    poller.setInput("current", true)
    await settle()
    poller.setInput("next", false)
    t.mock.timers.tick(1999); await settle()
    assert.equal(calls, 1)
    t.mock.timers.tick(1); await settle()
    assert.equal(calls, 2)
    poller.setInput("next", true)
    await settle()
    assert.equal(calls, 3)
  } finally { poller.stop() }
})

test("unmount aborts an outstanding request and suppresses its result and future polling", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] })
  let signal!: AbortSignal
  let resolve!: (items: ContinuationStatus[]) => void
  let calls = 0
  const poller = createWorkContinuationPolling({
    load: nextSignal => { calls++; signal = nextSignal; return new Promise(done => { resolve = done }) },
    receive: () => assert.fail("disposed status was published"),
  })
  poller.setInput("current", true)
  poller.stop()
  assert.equal(signal.aborted, true)
  resolve([continuation()])
  await settle()
  poller.refresh()
  poller.setInput("next", true)
  t.mock.timers.tick(10000); await settle()
  assert.equal(calls, 1)
})

test("the page pairs confirmation with the current input rather than an earlier receipt", () => {
  const source = ts.createSourceFile("task-detail.tsx", readFileSync(new URL(
    "../src/pages/console/user/task/task-detail.tsx", import.meta.url,
  ), "utf8"), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  let currentInput: ts.Expression | undefined
  let props: ts.JsxAttributes | undefined
  const visit = (node: ts.Node) => {
    if (ts.isVariableDeclaration(node) && node.name.getText(source) === "latestLiveUserInput") currentInput = node.initializer
    if (ts.isJsxSelfClosingElement(node) && node.tagName.getText(source) === "WorkTaskStatus") props = node.attributes
    ts.forEachChild(node, visit)
  }
  visit(source)
  assert.ok(currentInput)
  assert.ok(props)
  const expression = (name: string) => {
    const prop = props.properties.find(item => ts.isJsxAttribute(item) && item.name.getText(source) === name)
    assert.ok(prop && ts.isJsxAttribute(prop) && prop.initializer && ts.isJsxExpression(prop.initializer) && prop.initializer.expression)
    return prop.initializer.expression.getText(source)
  }
  const context = vm.createContext({
    liveMessages: [
      { id: "old", data: { clientMessageId: "previous", deliveryState: "confirmed" } },
      { id: "new", data: { clientMessageId: "current", deliveryState: "sending" } },
    ],
    latestLiveUserInputId: "new",
  })
  const compiled = ts.transpileModule(`globalThis.select = () => {
    const latestLiveUserInput = (${currentInput.getText(source)});
    return [${expression("messageId")}, ${expression("inputConfirmed")}];
  };`, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None } }).outputText
  vm.runInContext(compiled, context)
  assert.deepEqual([...context.select()], ["current", false])
  context.liveMessages[1].data.deliveryState = "confirmed"
  assert.deepEqual([...context.select()], ["current", true])
  context.latestLiveUserInputId = null
  assert.deepEqual([...context.select()], [undefined, false])
})

// Exercise the component's actual storage effect and dependency list. Replacing
// only request/state boundaries keeps this check independent of the scheduler.
test("new input does not restart storage polling or change its five second interval", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] })
  const source = ts.createSourceFile("work-task-status.tsx", readFileSync(new URL(
    "../src/components/console/work/work-task-status.tsx", import.meta.url,
  ), "utf8"), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  let effect: ts.CallExpression | undefined
  const visit = (node: ts.Node) => {
    if (ts.isCallExpression(node) && node.expression.getText(source) === "useEffect" && node.arguments[0]?.getText(source).includes("/storage-status")) effect = node
    ts.forEachChild(node, visit)
  }
  visit(source)
  assert.ok(effect)
  let calls = 0
  const context = vm.createContext({
    taskId: "task", refresh: 0, messageId: "previous", inputConfirmed: true,
    AbortController, setTimeout, clearTimeout, encodeURIComponent,
    workRequest: async () => { calls++; return { items: [] } }, setIssues: () => {},
  })
  const compiled = ts.transpileModule(`globalThis.start = (${effect.arguments[0].getText(source)}); globalThis.dependencies = () => (${effect.arguments[1].getText(source)});`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
  }).outputText
  vm.runInContext(compiled, context)
  const dependencies = [...context.dependencies()]
  const stop = context.start()
  try {
    await settle()
    context.messageId = "current"
    context.inputConfirmed = false
    assert.deepEqual([...context.dependencies()], dependencies)
    context.inputConfirmed = true
    assert.deepEqual([...context.dependencies()], dependencies)
    t.mock.timers.tick(4999); await settle()
    assert.equal(calls, 1)
    t.mock.timers.tick(1); await settle()
    assert.equal(calls, 2)
  } finally { stop() }
})
