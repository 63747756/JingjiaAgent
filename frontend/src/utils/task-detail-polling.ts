import { Api, type DomainProjectTask } from "../api/Api"

export type TaskDetailFailure = {
  kind: "retry" | "stop"
  message?: string
  status?: number
}

export type TaskDetailResult = { kind: "success"; task: DomainProjectTask } | TaskDetailFailure

// Permission/not-found and authentication errors defined by backend/errcode.
const TERMINAL_CODES = new Set([10001, 10002, 10603, 10605])
const RETRY_DELAYS_MS = [2000, 4000, 8000, 16000, 30000]

export function taskDetailFailure(error: unknown): TaskDetailFailure {
  const failure = error as { status?: number; code?: number; message?: string; error?: { code?: number; message?: string } } | null
  const status = failure?.status
  const code = failure?.error?.code ?? failure?.code
  const terminal = (status !== undefined && status >= 400 && status < 500 && ![408, 425, 429].includes(status))
    || (code !== undefined && (TERMINAL_CODES.has(code) || [400, 401, 403, 404, 410].includes(code)))
  return { kind: terminal ? "stop" : "retry", status, message: failure?.error?.message ?? failure?.message }
}

export async function loadTaskDetail(taskId: string, signal: AbortSignal): Promise<TaskDetailResult> {
  try {
    const response = await new Api().api.v1UsersTasksDetail(taskId, { signal })
    const task = response.data?.data
    if (response.data?.code === 0 && typeof task?.id === "string" && task.id) {
      return { kind: "success", task: { ...task, id: task.id } }
    }
    // Invalid/missing responses and server-side failures are retryable too.
    return taskDetailFailure(response.data)
  } catch (error) {
    return taskDetailFailure(error)
  }
}

export function startTaskDetailPolling({ loadTask, onTask, onFailure }: {
  loadTask: (signal: AbortSignal) => Promise<TaskDetailResult>
  onTask: (task: DomainProjectTask) => void
  onFailure?: (failure: TaskDetailFailure) => void
}) {
  const controller = new AbortController()
  let stopped = false
  let timer: ReturnType<typeof setTimeout> | undefined
  let failures = 0

  const poll = async () => {
    let result: TaskDetailResult
    try {
      result = await loadTask(controller.signal)
    } catch (error) {
      result = taskDetailFailure(error)
    }
    // An old request may complete after navigation, even if fetch ignores abort.
    if (stopped) return

    let delay: number
    if (result.kind === "success") {
      onTask(result.task)
      failures = 0
      delay = result.task.status === "pending" ? 2000 : result.task.status === "processing" ? 10000 : 60000
    } else {
      // Notify once per outage, rather than producing a toast on every retry.
      if (failures === 0 || result.kind === "stop") onFailure?.(result)
      if (result.kind === "stop") return
      delay = RETRY_DELAYS_MS[Math.min(failures++, RETRY_DELAYS_MS.length - 1)]
    }
    if (!stopped) timer = setTimeout(poll, delay)
  }

  void poll()
  return () => {
    stopped = true
    controller.abort()
    clearTimeout(timer)
  }
}
