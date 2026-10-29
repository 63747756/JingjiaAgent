import assert from "node:assert/strict"
import test from "node:test"
import type { MessageType } from "../src/components/console/task/message.tsx"
import { TaskMessageHandler, type TaskMessageRawChunk } from "../src/components/console/task/task-message-handler.ts"
import { buildTaskRoundDisplayRows, getTaskRoundDuration, type TaskRound } from "../src/components/console/task/task-round-display.ts"
import { b64encode } from "../src/utils/message-data.ts"

const startedAt = 1780000000000
const complete: TaskRound = { id: "turn-1", turnSeq: 1, status: "complete", startedAt, endedAt: startedAt + 24000 }
function message(id: string, type: MessageType["type"], content = "", round = complete): MessageType {
  return { id, type, role: type === "user_input" ? "user" : "agent", time: startedAt, data: { content }, round }
}
function fixture(round = complete) {
  return [
    message("input", "user_input", "Check the file", round),
    message("intro", "agent_message_chunk", "I will read the file.", round),
    message("tool", "tool_call", "", round),
    message("stream", "agent_message_chunk", "Checks passed.\n\nFinal answer.", round),
    { ...message("final", "agent_message_chunk", "Final answer.", round), isFinal: true },
  ]
}
const messagesOf = (rows: ReturnType<typeof buildTaskRoundDisplayRows>) => rows.flatMap(row => row.type === "message" ? [row.message] : [])

test("successful turns default to one duration row and the independent final reply", () => {
  const rows = buildTaskRoundDisplayRows(fixture())
  assert.deepEqual(rows.map(row => row.key), ["input", "round-summary-turn-1", "final"])
  assert.equal(rows[1].type === "round-summary" && rows[1].expanded, false)
  assert.equal(rows[2].type === "message" && rows[2].placement, "final")
  assert.equal(getTaskRoundDuration(complete), 24000)
})

test("expanding restores process, retains same-message progress and removes the final echo", () => {
  const source = fixture()
  const rows = buildTaskRoundDisplayRows(source, { "turn-1": { status: "complete", expanded: true } })
  const shown = messagesOf(rows)
  assert.deepEqual(shown.map(item => item.id), ["input", "intro", "tool", "stream", "final"])
  assert.equal(shown.find(item => item.id === "stream")?.data.content, "Checks passed.")
  assert.equal(shown.filter(item => item.data.content?.includes("Final answer.")).length, 1)
  assert.equal(source[3].data.content, "Checks passed.\n\nFinal answer.", "view building must not mutate stored messages")
})

test("an exact streamed answer disappears entirely from expanded process", () => {
  const source = fixture()
  source[3].data.content = "Final answer.\n"
  assert.equal(messagesOf(buildTaskRoundDisplayRows(source, { "turn-1": { status: "complete", expanded: true } })).some(item => item.id === "stream"), false)
})

test("failure, cancellation and unknown terminal status remain expanded by default", () => {
  for (const status of ["failed", "canceled", "unknown"] as const) {
    const rows = buildTaskRoundDisplayRows(fixture({ ...complete, status }))
    assert.equal(rows[1].type === "round-summary" && rows[1].expanded, true)
    assert.ok(messagesOf(rows).some(item => item.id === "tool"))
  }
})

test("running and disconnected turns never collapse without a server terminal state", () => {
  const running = { ...complete, status: "running" as const, endedAt: undefined }
  const rows = buildTaskRoundDisplayRows(fixture(running))
  assert.ok(rows.every(row => row.type === "message"))
  assert.ok(messagesOf(rows).some(item => item.id === "tool"))
  assert.equal(getTaskRoundDuration(running), null)
})

test("new completion ignores expansion state from an earlier running state", () => {
  const rows = buildTaskRoundDisplayRows(fixture(), { "turn-1": { status: "running", expanded: true } })
  assert.equal(rows[1].type === "round-summary" && rows[1].expanded, false)
})

test("errors, warnings and unanswered questions remain visible even in manually collapsed turns", () => {
  const source = fixture()
  source.splice(2, 0,
    message("error", "error_message", "Failed read"),
    message("warning", "alert_message", "Review needed"),
    { ...message("question", "ask_user_question"), data: { status: "pending" } },
    { ...message("answered", "ask_user_question"), data: { status: "completed" } },
  )
  assert.deepEqual(messagesOf(buildTaskRoundDisplayRows(source)).map(item => item.id), ["input", "error", "warning", "question", "final"])
})

test("success without a final marker expands all process text without a placeholder", () => {
  const source = fixture().filter(item => !item.isFinal)
  const rows = buildTaskRoundDisplayRows(source)
  assert.deepEqual(rows.map(row => row.key), ["input", "round-summary-turn-1", "intro", "tool", "stream"])
  assert.equal(rows[1].type === "round-summary" && rows[1].expanded, true)
  assert.equal(rows.some(row => row.type === "message" && row.placement === "final"), false)
  assert.ok(rows.some(row => row.type === "message" && row.key === "stream" && row.placement === "process"))
})

test("success without a final reply still allows manual collapse and re-expansion", () => {
  const source = fixture().filter(item => !item.isFinal)
  const collapsed = buildTaskRoundDisplayRows(source, { "turn-1": { status: "complete", expanded: false } })
  assert.deepEqual(collapsed.map(row => row.key), ["input", "round-summary-turn-1"])
  assert.equal(collapsed[1].type === "round-summary" && collapsed[1].expanded, false)
  assert.deepEqual(collapsed[1].type === "round-summary" && collapsed[1].processMessageIds, ["intro", "tool", "stream"])

  const expanded = buildTaskRoundDisplayRows(source, { "turn-1": { status: "complete", expanded: true } })
  assert.equal(expanded[1].type === "round-summary" && expanded[1].expanded, true)
  assert.deepEqual(messagesOf(expanded).map(item => item.id), ["input", "intro", "tool", "stream"])
})

test("completion without a final reply keeps the running process visible", () => {
  const running = { ...complete, status: "running" as const, endedAt: undefined }
  const source = fixture(running).filter(item => !item.isFinal)
  const before = buildTaskRoundDisplayRows(source)
  assert.deepEqual(messagesOf(before).map(item => item.id), ["input", "intro", "tool", "stream"])

  const after = buildTaskRoundDisplayRows(source.map(item => ({ ...item, round: complete })), {
    "turn-1": { status: "running", expanded: false },
  })
  assert.equal(after[1].type === "round-summary" && after[1].expanded, true)
  assert.deepEqual(messagesOf(after).map(item => item.id), messagesOf(before).map(item => item.id))
})

test("success without any agent output shows only the input and duration row", () => {
  const rows = buildTaskRoundDisplayRows([message("input", "user_input", "Check")])
  assert.deepEqual(rows.map(row => row.key), ["input", "round-summary-turn-1"])
  assert.deepEqual(rows[1].type === "round-summary" && rows[1].processMessageIds, [])
})

test("completed event streams with missing, empty or invalid final replies never promote progress text", () => {
  const encode = (value: unknown) => b64encode(JSON.stringify(value))
  const progressText = "正在检查文件，请稍等。"
  for (const finalPayload of [undefined, { content: "" }, { content: "  \n" }, { content: null }, {}]) {
    const chunks: TaskMessageRawChunk[] = [
      { event: "user-input", turn_seq: 1, seq: 1, timestamp: startedAt, data: encode({ content: b64encode("检查文件"), attachments: [] }) },
      { event: "task-running", kind: "acp_event", turn_seq: 1, seq: 2, timestamp: startedAt + 1000,
        data: encode({ update: { sessionUpdate: "agent_message_chunk", content: { type: "text", text: progressText } } }) },
    ]
    if (finalPayload !== undefined) {
      chunks.push({ event: "agent-final", turn_seq: 1, seq: 3, timestamp: startedAt + 23000, data: encode(finalPayload) })
    }
    chunks.push({ event: "task-ended", turn_seq: 1, seq: 4, timestamp: startedAt + 24000, data: encode({ status: "complete" }) })
    const state = new TaskMessageHandler().pushChunks(chunks)
    assert.equal(state.messages[0].round?.status, "complete")

    const rows = buildTaskRoundDisplayRows(state.messages)
    assert.equal(rows.length, 3, "there should be only input, summary and existing progress text")
    assert.equal(rows[1].type === "round-summary" && rows[1].expanded, true)
    assert.equal(rows.some(row => row.type === "message" && row.placement === "final"), false)
    assert.equal(messagesOf(rows).at(-1)?.data.content, progressText)
  }
})

test("an explicitly empty final does not promote progress text to the answer", () => {
  const rows = buildTaskRoundDisplayRows([
    message("input", "user_input", "Check"),
    message("intro", "agent_message_chunk", "正在检查"),
    { ...message("final", "agent_message_chunk", ""), isFinal: true },
  ])
  assert.deepEqual(rows.map(row => row.key), ["input", "round-summary-turn-1", "intro"])
  assert.equal(rows[1].type === "round-summary" && rows[1].expanded, true)
})

test("trailing plan snapshots do not promote ordinary text to a final reply", () => {
  const plan = { ...message("plan", "plan"), data: { entries: [{ content: "检查", priority: "medium", status: "completed" }] } }
  const rows = buildTaskRoundDisplayRows([...fixture().filter(item => !item.isFinal), plan])
  assert.deepEqual(messagesOf(rows).map(item => item.id), ["input", "intro", "tool", "stream", "plan"])
  assert.equal(rows[1].type === "round-summary" && rows[1].expanded, true)
  assert.ok(rows.some(row => row.type === "round-summary" && row.processMessageIds.includes("plan")))
})

test("multiple turns preserve independent expansion and final answers after history prepend", () => {
  const older = fixture({ ...complete, id: "turn-0", turnSeq: 0 }).map(item => ({ ...item, id: `older-${item.id}` }))
  const rows = buildTaskRoundDisplayRows([...older, ...fixture()], { "turn-1": { status: "complete", expanded: true } })
  assert.deepEqual(rows.filter(row => row.type === "round-summary").map(row => row.expanded), [false, true])
  assert.deepEqual(messagesOf(rows).filter(item => item.isFinal).map(item => item.id), ["older-final", "final"])
})

test("missing or invalid timestamps do not produce fabricated elapsed time", () => {
  assert.equal(getTaskRoundDuration({ ...complete, startedAt: undefined }), null)
  assert.equal(getTaskRoundDuration({ ...complete, endedAt: startedAt - 1 }), null)
  assert.equal(getTaskRoundDuration({ ...complete, startedAt: Number.NaN }), null)
  assert.equal(getTaskRoundDuration({ ...complete, endedAt: startedAt }), 0)
})

test("unmatched final text does not delete tools or earlier identical text", () => {
  const source = fixture()
  source[1].data.content = "Final answer."
  source[3].data.content = "A distinct activity update."
  const shown = messagesOf(buildTaskRoundDisplayRows(source, { "turn-1": { status: "complete", expanded: true } }))
  assert.equal(shown.find(item => item.id === "intro")?.data.content, "Final answer.")
  assert.equal(shown.find(item => item.id === "stream")?.data.content, "A distinct activity update.")
  assert.ok(shown.some(item => item.id === "tool"))
})

test("an exact final suffix spanning text blocks removes only echoed text and keeps tools", () => {
  const source = [message("input", "user_input", "检查"), message("first", "agent_message_chunk", "开始检查。中文"),
    message("tool", "tool_call"), message("second", "agent_message_chunk", "回答"),
    { ...message("final", "agent_message_chunk", "中文回答"), isFinal: true }]
  const shown = messagesOf(buildTaskRoundDisplayRows(source, { "turn-1": { status: "complete", expanded: true } }))
  assert.deepEqual(shown.map(item => [item.id, item.data.content]), [["input", "检查"], ["first", "开始检查。"], ["tool", ""], ["final", "中文回答"]])
})
