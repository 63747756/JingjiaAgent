import type { ContinuationStatus } from "./work-task-status-state"

type InputState = { messageId?: string; confirmed: boolean }

export function createWorkContinuationPolling({ load, receive }: {
  load: (signal: AbortSignal) => Promise<ContinuationStatus[]>
  receive: (items: ContinuationStatus[]) => void
}) {
  let input: InputState | undefined
  const confirmedMessages = new Set<string>()
  let generation = 0
  let stopped = false
  let controller: AbortController | undefined
  let timer: ReturnType<typeof setTimeout> | undefined
  let refreshPending = false

  const clearTimer = () => { clearTimeout(timer); timer = undefined }
  const invalidate = () => {
    generation++
    clearTimer()
    controller?.abort()
    refreshPending = false
  }
  const schedule = (delay: number) => {
    clearTimer()
    if (!stopped) timer = setTimeout(() => void poll(), delay)
  }
  const poll = async () => {
    if (stopped || controller) return
    clearTimer()
    refreshPending = false
    const request = new AbortController()
    const requestGeneration = generation
    controller = request
    let delay = 2000
    try {
      const items = await load(request.signal)
      if (stopped || request.signal.aborted || requestGeneration !== generation) return
      receive(items)
      const messageId = input?.confirmed ? input.messageId : undefined
      const current = messageId
        ? items.find(item => item.message_id === messageId)
        : undefined
      if (current?.phase === "starting" && (current.state === "pending" || current.state === "submitting")) delay = 1000
    } catch { /* Keep known status; transient failures return to the normal interval. */ }
    finally {
      controller = undefined
      if (!stopped) {
        // An aborted fetch may settle late. Wait for it before the next request,
        // and never publish its result into another round.
        if (refreshPending) void poll()
        else schedule(requestGeneration === generation ? delay : 2000)
      }
    }
  }
  const refresh = () => {
    if (stopped) return
    invalidate()
    refreshPending = true
    if (!controller) void poll()
  }

  return {
    setInput(messageId: string | undefined, confirmed: boolean) {
      if (stopped) return
      const initial = input === undefined
      const changed = input?.messageId !== messageId
      input = { messageId, confirmed }
      const newlyConfirmed = !!messageId && confirmed && !confirmedMessages.has(messageId)
      if (newlyConfirmed && messageId) confirmedMessages.add(messageId)
      if (initial || newlyConfirmed) refresh()
      else if (changed) {
        invalidate()
        if (!controller) schedule(2000)
      }
    },
    refresh,
    stop() { stopped = true; invalidate() },
  }
}
