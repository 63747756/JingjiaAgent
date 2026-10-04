import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import test from "node:test"
import type { DomainProjectTask } from "../src/api/Api.ts"
import type { TaskControlCallResponse, TaskControlOperation } from "../src/components/console/task/task-control-client.ts"
import { applyTaskControlResult, isSuccessfulControlResult, repairTaskError, taskControlNoticeKey } from "../src/components/console/task/task-control-ui.ts"
import cn from "../src/i18n/resources/cn.ts"
import en from "../src/i18n/resources/en.ts"

const source = (file: string) => readFileSync(new URL(`../src/${file}`, import.meta.url), "utf8")
const page = source("pages/console/user/task/task-detail.tsx")
const skills = source("components/console/task/task-skills-update-dialog.tsx")
const error = source("components/console/task/message-error.tsx")
const chat = source("components/console/task/chat-inputbox.tsx")
const operation: TaskControlOperation = {
  requestId: "same-request", kind: "switch_model", payload: { model_id: "new-model", load_session: true }, status: "waiting",
}
const task = { id: "task", model: { id: "old-model", model: "old" }, extra: { skill_ids: ["old-skill"], plugin_ids: ["plugin"] } } as DomainProjectTask

test("control notice distinguishes waiting, accepted, uncertain, and reconnecting without declaring failure", () => {
  assert.equal(taskControlNoticeKey({ status: "connected", operation: null }), null)
  for (const status of ["waiting", "pending", "uncertain"] as const) {
    assert.equal(taskControlNoticeKey({ status: "connected", operation: { ...operation, status } }), `taskDetail.page.control.${status}`)
    assert.equal(taskControlNoticeKey({ status: "error", operation: { ...operation, status } }), "taskDetail.page.control.reconnecting")
  }
  for (const locale of [cn, en]) {
    for (const key of ["waiting", "pending", "uncertain", "reconnecting", "closeHint", "unavailable"] as const) assert.ok(locale.taskDetail.page.control[key])
  }
})

test("only an acknowledged terminal success applies a model or resource selection", () => {
  for (const response of [null, undefined, { status: "pending", success: true }, { status: "detached", success: true }, { status: "failed", success: true }, { success: false }] as (TaskControlCallResponse | null | undefined)[]) {
    assert.equal(isSuccessfulControlResult(response), false)
    if (response) assert.equal(applyTaskControlResult(task, operation, response, [{ id: "new-model" }]), task)
  }
  assert.equal(isSuccessfulControlResult({ status: "succeeded", success: true }), true)
  assert.equal(isSuccessfulControlResult({ success: true }), true)
  const selected = { id: "new-model", model: "New model name" }
  const updated = applyTaskControlResult(task, operation, { success: true, status: "succeeded", model: { id: "new-model" } }, [selected])
  assert.deepEqual(updated?.model, selected, "an ID-only recovered result preserves model display metadata")
  assert.deepEqual(applyTaskControlResult(task, operation, { success: true, model: { id: "new-model", model: "", provider: "", context_limit: 0 } }, [selected])?.model, selected)
  assert.equal(task.model?.id, "old-model", "the old task is not mutated")
  const resourceOperation: TaskControlOperation = { ...operation, kind: "switch_agent_resources", payload: { skill_ids: ["new-skill"], plugin_ids: ["plugin"] } }
  assert.deepEqual(applyTaskControlResult(task, resourceOperation, { success: true, status: "succeeded" }, [])?.extra, { skill_ids: ["new-skill"], plugin_ids: ["plugin"] })
  assert.equal(applyTaskControlResult(task, resourceOperation, { status: "pending" }, []), task)
})

test("error repair waits for terminal completion and sends its continuation exactly once", async () => {
  let resolve!: (response: TaskControlCallResponse) => void
  const response = new Promise<TaskControlCallResponse>((done) => { resolve = done })
  let sends = 0
  const result = repairTaskError({ reload: () => response, send: () => { sends++; return true }, isCurrent: () => true })
  await Promise.resolve()
  assert.equal(sends, 0)
  resolve({ success: true, status: "succeeded" })
  assert.equal(await result, "succeeded")
  assert.equal(sends, 1)
})

test("leaving the task or a detached operation never sends a continuation or reports repair failure", async () => {
  for (const [response, current] of [
    [{ status: "detached" }, true], [{ status: "succeeded", success: true }, false], [{ status: "failed", success: false }, false],
  ] as [TaskControlCallResponse, boolean][]) {
    let sends = 0
    assert.equal(await repairTaskError({ reload: async () => response, send: () => { sends++; return true }, isCurrent: () => current }), "detached")
    assert.equal(sends, 0)
  }
  let active = true
  let finish!: (response: TaskControlCallResponse) => void
  const response = new Promise<TaskControlCallResponse>((resolve) => { finish = resolve })
  const result = repairTaskError({ reload: () => response, send: () => assert.fail("old task must not send"), isCurrent: () => active })
  active = false
  finish({ status: "succeeded", success: true })
  assert.equal(await result, "detached")
})

test("error repair does not continue on pending or failed replies", async () => {
  for (const response of [{ status: "pending", success: true }, { status: "failed", success: false }] as TaskControlCallResponse[]) {
    assert.equal(await repairTaskError({ reload: async () => response, send: () => assert.fail("unconfirmed result must not send"), isCurrent: () => true }), "failed")
  }
  assert.equal(await repairTaskError({ reload: async () => ({ success: true }), send: () => false, isCurrent: () => true }), "failed")
})

test("control confirmations remain dismissible and page owns result handling", () => {
  assert.doesNotMatch(page, /if \((?:modelSwitch|resetContext|restartAgent)Submitting\) return/)
  assert.doesNotMatch(page, /<AlertDialogCancel[^>]*disabled=/)
  assert.doesNotMatch(skills, /submitting && !nextOpen/)
  assert.match(skills, /<Dialog open=\{open\} onOpenChange=\{onOpenChange\}>/)
  assert.match(skills, /onClick=\{\(\) => onOpenChange\(false\)\}/)
  assert.match(skills, /disabled=\{disabled \|\| submitting \|\| loading\}/)
  assert.doesNotMatch(skills, /response\.success|toast\.success|toast\.timeout/)
  assert.match(page, /onOperationResult: \(operation, response\)/)
  assert.match(page, /applyTaskControlResult\(previous, operation, response, models\)/)
  assert.match(page, /role="status" aria-live="polite"/)
})

test("new controls use the synchronous operation latch and account-scoped lifecycle guards", () => {
  assert.match(page, /client\?\.getState\(\)\.operation/)
  assert.match(page, /TaskControlClient\.hasStoredOperation\(taskId, user\.id\)/)
  assert.match(page, /userId: user\.id/)
  assert.match(page, /taskControlClientRef\.current !== client \|\| taskIdentityRef\.current !== taskIdentity/)
  assert.match(page, /TaskControlClient\.clearStoredOperation\(taskId, user\.id\)/)
  assert.match(page, /failure\.kind === "stop"/)
  assert.match(page, /pollControlVersion === controlResultVersionRef\.current/)
  assert.doesNotMatch(page, /\.restart\(/)
  assert.match(error, /if \(repairingRef\.current \|\| message\.controlBusy\) return/)
  assert.match(error, /if \(result === "detached"\) return/)
  assert.match(chat, /const inputLocked = autoSendingQueuedInput \|\| longContentConverting \|\| controlBusy/)
})
