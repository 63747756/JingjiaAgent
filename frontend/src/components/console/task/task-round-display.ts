import type { MessageType } from "./message"

export type TaskRound = NonNullable<MessageType["round"]>
export type TaskRoundExpansion = Record<string, { status: TaskRound["status"]; expanded: boolean }>

export type TaskRoundDisplayRow =
  | { type: "message"; key: string; message: MessageType; roundId?: string; placement: "input" | "process" | "final" | "visible" }
  | { type: "round-summary"; key: string; round: TaskRound; expanded: boolean; processMessageIds: string[] }

export function isTaskRoundExpanded(round: TaskRound, expansion: TaskRoundExpansion, hasFinalReply: boolean) {
  const override = expansion[round.id]
  return override?.status === round.status ? override.expanded : round.status !== "complete" || !hasFinalReply
}

export function getTaskRoundDuration(round: TaskRound): number | null {
  if (!Number.isFinite(round.startedAt) || !Number.isFinite(round.endedAt)
    || round.startedAt! <= 0 || round.endedAt! < round.startedAt!) return null
  return round.endedAt! - round.startedAt!
}

function hasText(message: MessageType) {
  return message.type === "agent_message_chunk" && typeof message.data.content === "string"
    && message.data.content.trim() !== "" && message.data.content.trim() !== "(no content)"
}

function isAlwaysVisible(message: MessageType) {
  return message.type === "error_message" || message.type === "alert_message"
    || message.type === "restart_session"
    || (message.type === "ask_user_question" && !["completed", "expired"].includes(message.data.status ?? ""))
}

// The final event carries the complete answer as well as the streamed deltas.
// Remove only an exact suffix of the streamed text (which can be interrupted
// by tools/thoughts). Keep those events and any earlier progress text intact.
function removeFinalEcho(messages: MessageType[], finalText: string) {
  const trimmedFinal = finalText.trim()
  if (!trimmedFinal) return messages
  const textOf = (message: MessageType): string => message.type === "agent_message_chunk" && typeof message.data.content === "string" ? message.data.content : ""
  const fullText = messages.map(textOf).join("").trimEnd()
  if (!fullText.endsWith(trimmedFinal)) return messages
  let remaining = fullText.length - trimmedFinal.length
  return messages.flatMap(message => {
    const text = textOf(message)
    if (!text) return [message]
    const keep = Math.min(text.length, remaining)
    remaining -= keep
    if (keep === text.length) return [message]
    const prefix = text.slice(0, keep).trimEnd()
    return prefix ? [{ ...message, data: { ...message.data, content: prefix } }] : []
  })
}

export function buildTaskRoundDisplayRows(messages: MessageType[], expansion: TaskRoundExpansion = {}): TaskRoundDisplayRow[] {
  const groups: { round?: TaskRound; messages: MessageType[] }[] = []
  const byRound = new Map<string, typeof groups[number]>()
  for (const message of messages) {
    if (!message.round) {
      groups.push({ messages: [message] })
      continue
    }
    let group = byRound.get(message.round.id)
    if (!group) {
      group = { round: message.round, messages: [] }
      byRound.set(message.round.id, group)
      groups.push(group)
    }
    group.round = message.round
    group.messages.push(message)
  }

  const rows: TaskRoundDisplayRow[] = []
  for (const group of groups) {
    const { round } = group
    const addMessage = (message: MessageType, placement: "input" | "process" | "final" | "visible") => {
      rows.push({ type: "message", key: message.id, message, roundId: round?.id, placement })
    }
    if (!round) {
      group.messages.forEach(message => addMessage(message, "visible"))
      continue
    }

    const inputs = group.messages.filter(message => message.type === "user_input")
    let body = group.messages.filter(message => message.type !== "user_input" && !message.isFinal)
    const final = [...group.messages].reverse().find(message => message.isFinal && hasText(message))
    if (final) {
      body = removeFinalEcho(body, final.data.content)
    }

    inputs.forEach(message => addMessage(message, "input"))
    const ended = round.status !== "running" && round.endedAt !== undefined
    if (!ended) {
      body.forEach(message => addMessage(message, "visible"))
      if (final) addMessage(final, "visible")
      continue
    }

    const process = body.filter(message => !isAlwaysVisible(message))
    const expanded = isTaskRoundExpanded(round, expansion, Boolean(final))
    rows.push({ type: "round-summary", key: `round-summary-${round.id}`, round, expanded, processMessageIds: process.map(message => message.id) })
    for (const message of body) {
      if (isAlwaysVisible(message)) addMessage(message, "visible")
      else if (expanded) addMessage(message, "process")
    }
    if (final) addMessage(final, "final")
  }
  return rows
}
