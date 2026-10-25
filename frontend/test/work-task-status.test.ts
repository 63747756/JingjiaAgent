import assert from "node:assert/strict"
import test from "node:test"
import { isPendingContinuation, selectWorkTaskStatus, type ContinuationStatus, type StorageIssue } from "../src/components/console/work/work-task-status-state.ts"

function continuation(overrides: Partial<ContinuationStatus> = {}): ContinuationStatus {
  return { id: "command-current", message_id: "message-current", state: "pending", phase: "starting", ...overrides }
}

function storage(overrides: Partial<StorageIssue> = {}): StorageIssue {
  return { id: "storage-current", kind: "checkpoint", error: "Snapshot could not be saved", attempts: 3, paused: true, ...overrides }
}

test("each preparation phase remains preparation until the backend confirms running", () => {
  for (const phase of ["queued", "saving", "restoring", "preparing", "starting", "waking"]) {
    const item = continuation({ phase, state: phase === "queued" || phase === "saving" ? "waiting" : "pending" })
    const selected = selectWorkTaskStatus([item], [], item.message_id, true)
    assert.equal(selected.kind, "preparing", phase)
    assert.equal(selected.continuation?.phase, phase)
    assert.equal(selected.storage, undefined)
  }
})

test("backend running wins over its retained preparation phase", () => {
  const item = continuation({ state: "running", phase: "starting" })
  assert.equal(selectWorkTaskStatus([item], [], item.message_id, true).kind, "running")
})

test("preparation failure preserves the error and recovery checkpoint for retry actions", () => {
  const item = continuation({ state: "blocked", phase: "failed", error: "Choose an older checkpoint", checkpoint_id: "checkpoint-previous" })
  const selected = selectWorkTaskStatus([item], [], item.message_id, true)
  assert.equal(selected.kind, "blocked")
  assert.equal(selected.continuation, item)
  assert.equal(selected.continuation?.error, "Choose an older checkpoint")
  assert.equal(selected.continuation?.checkpoint_id, "checkpoint-previous")
})

test("a failed save and its blocked continuation select one storage status with both retry identities", () => {
  const item = continuation({ state: "blocked", phase: "saving", error: "Previous turn has not been saved" })
  const issue = storage()
  const selected = selectWorkTaskStatus([item], [issue], item.message_id, true)
  assert.equal(selected.kind, "storage")
  assert.equal(selected.continuation, item)
  assert.equal(selected.storage, issue)
})

test("an automatically retrying save also occupies the saving continuation status", () => {
  const item = continuation({ state: "waiting", phase: "saving" })
  const issue = storage({ attempts: 1, paused: false })
  const selected = selectWorkTaskStatus([item], [issue], item.message_id, true)
  assert.equal(selected.kind, "storage")
  assert.equal(selected.storage?.paused, false)
})

test("execution retains unrelated artifact failure information in the same selected status", () => {
  const item = continuation({ state: "running" })
  const issue = storage({ kind: "artifacts", error: "Document conversion failed" })
  const selected = selectWorkTaskStatus([item], [issue], item.message_id, true)
  assert.equal(selected.kind, "running")
  assert.equal(selected.continuation, item)
  assert.equal(selected.storage, issue)
})

test("an independent preparation failure keeps recovery actions alongside storage errors", () => {
  const item = continuation({ state: "blocked", phase: "restoring", checkpoint_id: "checkpoint-previous" })
  const issue = storage({ kind: "artifacts" })
  const selected = selectWorkTaskStatus([item], [issue], item.message_id, true)
  assert.equal(selected.kind, "blocked")
  assert.equal(selected.continuation?.checkpoint_id, "checkpoint-previous")
  assert.equal(selected.storage, issue)
})

test("checkpoint failure takes priority over a separate artifact failure", () => {
  const artifact = storage({ id: "artifact-job", kind: "artifacts" })
  const checkpoint = storage()
  const selected = selectWorkTaskStatus([], [artifact, checkpoint], undefined, false)
  assert.equal(selected.kind, "storage")
  assert.equal(selected.storage, checkpoint)
})

test("the current message selects its own preparation instead of an earlier running turn", () => {
  const earlier = continuation({ id: "command-earlier", message_id: "message-earlier", state: "running" })
  const current = continuation({ state: "waiting", phase: "queued" })
  const selected = selectWorkTaskStatus([earlier, current], [], current.message_id, true)
  assert.equal(selected.kind, "preparing")
  assert.equal(selected.continuation, current)
})

test("an earlier blocked message does not replace the current execution", () => {
  const earlier = continuation({ id: "command-earlier", message_id: "message-earlier", state: "blocked", phase: "failed" })
  const current = continuation({ state: "running" })
  const selected = selectWorkTaskStatus([earlier, current], [], current.message_id, true)
  assert.equal(selected.kind, "running")
  assert.equal(selected.continuation, current)
})

test("a queued message exposes the earliest blocked predecessor and its recovery identity", () => {
  const firstBlocked = continuation({ id: "command-first-blocked", message_id: "message-first-blocked", state: "blocked", phase: "failed", checkpoint_id: "checkpoint-earlier" })
  const nextBlocked = continuation({ id: "command-next-blocked", message_id: "message-next-blocked", state: "blocked", phase: "failed", checkpoint_id: "checkpoint-next" })
  for (const state of ["waiting", "pending", "blocked"]) {
    const current = continuation({ state, phase: "queued" })
    const selected = selectWorkTaskStatus([firstBlocked, nextBlocked, current], [], current.message_id, true)
    assert.equal(selected.kind, "blocked", state)
    assert.equal(selected.continuation?.id, firstBlocked.id, state)
    assert.equal(selected.continuation?.checkpoint_id, firstBlocked.checkpoint_id, state)
  }
})

test("a blocked message after the current queued message cannot replace its status", () => {
  const current = continuation({ state: "waiting", phase: "queued" })
  const laterBlocked = continuation({ id: "command-later", message_id: "message-later", state: "blocked", phase: "failed", checkpoint_id: "checkpoint-later" })
  const selected = selectWorkTaskStatus([current, laterBlocked], [], current.message_id, true)
  assert.equal(selected.kind, "preparing")
  assert.equal(selected.continuation, current)
  assert.equal(selected.continuation?.checkpoint_id, undefined)
})

test("canceling the newest queued message leaves an earlier blocked recovery accessible", () => {
  const remainingBlocked = continuation({ id: "command-blocked", message_id: "message-blocked", state: "blocked", phase: "failed", checkpoint_id: "checkpoint-retained" })
  const selected = selectWorkTaskStatus([remainingBlocked], [], "message-canceled", false)
  assert.equal(selected.kind, "blocked")
  assert.equal(selected.continuation?.id, remainingBlocked.id)
  assert.equal(selected.continuation?.checkpoint_id, remainingBlocked.checkpoint_id)
})

test("a stale response for another message does not claim the new turn is executing", () => {
  const earlier = continuation({ message_id: "message-earlier", state: "running" })
  const selected = selectWorkTaskStatus([earlier], [], "message-new", true)
  assert.equal(selected.kind, "preparing")
  assert.equal(selected.continuation, undefined)
})

test("an active turn without a known continuation remains preparing", () => {
  for (const messageId of [undefined, "message-new"]) {
    const selected = selectWorkTaskStatus([], [], messageId, true)
    assert.equal(selected.kind, "preparing")
    assert.equal(selected.continuation, undefined)
  }
  const unknown = continuation({ state: "new-server-state", phase: "new-server-phase" })
  assert.equal(selectWorkTaskStatus([unknown], [], unknown.message_id, true).kind, "preparing")
})

test("attachment before message identity is available can display the server continuation", () => {
  const item = continuation({ state: "blocked", phase: "failed" })
  const selected = selectWorkTaskStatus([item], [], undefined, false)
  assert.equal(selected.kind, "blocked")
  assert.equal(selected.continuation, item)
})

test("finished turns leave the status area once server work and storage notices are clear", () => {
  assert.equal(selectWorkTaskStatus([], [], "message-finished", false).kind, "idle")
  assert.equal(selectWorkTaskStatus([], [], undefined, false).kind, "idle")
  const earlier = continuation({ message_id: "message-earlier", state: "running" })
  assert.equal(selectWorkTaskStatus([earlier], [], "message-finished", false).kind, "idle")
})

test("idle tasks retain actionable storage failures", () => {
  const issue = storage()
  const selected = selectWorkTaskStatus([], [issue], "message-finished", false)
  assert.equal(selected.kind, "storage")
  assert.equal(selected.storage, issue)
})

test("only waiting, blocked, and pending continuations request faster task polling", () => {
  for (const state of ["waiting", "blocked", "pending"]) {
    assert.equal(isPendingContinuation(continuation({ state })), true, state)
  }
  for (const state of ["running", "complete", "failed", "canceled", "", "unknown"]) {
    assert.equal(isPendingContinuation(continuation({ state })), false, state)
  }
})
