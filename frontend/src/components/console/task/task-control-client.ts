import { b64decode, b64encode } from "@/utils/message-data"
import type { RepoFileChange, RepoFileStatus, TaskRepositoryClient } from "./task-shared"

export type TaskControlClientStatus = "inited" | "connected" | "error"

export interface TaskControlClientState {
  status: TaskControlClientStatus
  operation: TaskControlOperation | null
}

export interface PortForwardInfo {
  port: number
  status: string
  process: string
  forward_id?: string | null
  access_url?: string | null
  label?: string | null
  error_message?: string | null
  whitelist_ips?: string[] | null
}

function decodeRepoFileContent(content: string): Uint8Array {
  if (content === "") {
    return new Uint8Array(0)
  }

  try {
    const binary = atob(content)
    const bytes = new Uint8Array(binary.length)
    for (let i = 0; i < binary.length; i++) {
      bytes[i] = binary.charCodeAt(i)
    }
    return bytes
  } catch {
    return new TextEncoder().encode(content)
  }
}

interface TaskControlCallMessage {
  type: "call"
  kind: string
  data: string
}

interface TaskControlPendingCall<T> {
  requestId: string
  resolve: (value: T | null | unknown) => void
  timeoutId: ReturnType<typeof setTimeout>
}

export interface TaskControlClientOptions {
  taskId: string
  userId?: string
  onOperationResult?: (operation: TaskControlOperation, response: TaskControlCallResponse) => void
  onStateChange?: (state: TaskControlClientState) => void
  onRepoFileChange?: () => void
  onPortChange?: (opened: boolean) => void
}

interface TaskControlStreamMessage {
  type?: string
  kind?: string
  data?: unknown
  timestamp?: number
}

export interface TaskControlCallResponse {
  request_id?: string
  success?: boolean
  error?: string | null
  message?: string
  status?: "pending" | "succeeded" | "failed" | "detached"
  model?: unknown
  session_id?: string
}

interface RestartTaskResponse extends TaskControlCallResponse {
  id?: string
  message?: string
  session_id?: string
}

interface SwitchModelResponse extends TaskControlCallResponse {
  id?: string
  message?: string
  session_id?: string
  model?: unknown
}

export interface SwitchAgentResourcesResponse extends TaskControlCallResponse {
  message?: string
  session_id?: string
}

export type TaskControlOperationKind = "restart" | "switch_model" | "switch_agent_resources"

export interface TaskControlOperation {
  requestId: string
  kind: TaskControlOperationKind
  payload: Record<string, unknown>
  status: "waiting" | "pending" | "uncertain"
}

interface ActiveOperation {
  operation: TaskControlOperation
  promise: Promise<TaskControlCallResponse>
  resolve: (response: TaskControlCallResponse) => void
  timer?: ReturnType<typeof setTimeout>
  retryTimer?: ReturnType<typeof setTimeout>
  retries: number
}

const OPERATION_RETRY_MS = [2000, 4000, 8000, 16000, 30000]
const operationKinds = new Set(["restart", "switch_model", "switch_agent_resources"])

// Persist only the original public selection, never arbitrary call payloads or
// execution configuration. Account + task scope prevents cross-login recovery.
function operationPayload(kind: string, value: unknown): Record<string, unknown> | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null
  const payload = value as Record<string, unknown>
  const validID = (id: unknown): id is string => typeof id === "string" && id.length > 0 && id.length <= 256
  if (kind === "restart" && typeof payload.load_session === "boolean") {
    return { load_session: payload.load_session }
  }
  if (kind === "switch_model" && validID(payload.model_id) && typeof payload.load_session === "boolean") {
    return { model_id: payload.model_id, load_session: payload.load_session }
  }
  if (kind === "switch_agent_resources" && [payload.skill_ids, payload.plugin_ids].every(
    ids => Array.isArray(ids) && ids.length <= 1000 && ids.every(validID),
  )) {
    return { skill_ids: [...payload.skill_ids as string[]], plugin_ids: [...payload.plugin_ids as string[]] }
  }
  return null
}

function operationStorageKey(taskId: string, userId?: string) {
  return userId ? `jingjiaagent:task-control-operation:${encodeURIComponent(userId)}:${encodeURIComponent(taskId)}` : null
}

function readStoredOperation(taskId: string, userId?: string): TaskControlOperation | null {
  const key = operationStorageKey(taskId, userId)
  if (!key) return null
  try {
    const raw = sessionStorage.getItem(key)
    if (!raw) return null
    const value = JSON.parse(raw)
    const payload = operationPayload(value.kind, value.payload)
    if (value.version !== 1 || value.taskId !== taskId || value.userId !== userId || !payload
      || typeof value.requestId !== "string" || !value.requestId || value.requestId.length > 256) {
      sessionStorage.removeItem(key)
      return null
    }
    return { requestId: value.requestId, kind: value.kind, payload, status: "uncertain" }
  } catch {
    try { sessionStorage.removeItem(key) } catch { /* Storage can be unavailable. */ }
    return null
  }
}

export class TaskControlClient implements TaskRepositoryClient {
  private static readonly CONNECT_TIMEOUT_MS = 10000
  private static readonly DEFAULT_CALL_TIMEOUT_MS = 5000
  private static readonly RESTART_TIMEOUT_MS = 15000
  private static readonly OPERATION_RESPONSE_TIMEOUT_MS = 35000

  private readonly taskId: string
  private readonly userId?: string
  private readonly onOperationResult?: TaskControlClientOptions["onOperationResult"]
  private readonly onStateChange?: (state: TaskControlClientState) => void
  private readonly onRepoFileChange?: () => void
  private readonly onPortChange?: (opened: boolean) => void

  private socket: WebSocket | null = null
  private connectTimeoutTimer: ReturnType<typeof setTimeout> | null = null
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private disposed = false
  private connectionId = 0
  private state: TaskControlClientState = {
    status: "inited",
    operation: null,
  }
  private operation: ActiveOperation | null = null
  private reconnectAttempts = 0
  private pendingCalls = new Map<string, TaskControlPendingCall<unknown>>()

  constructor({
    taskId,
    userId,
    onOperationResult,
    onStateChange,
    onRepoFileChange,
    onPortChange,
  }: TaskControlClientOptions) {
    this.taskId = taskId
    this.userId = userId
    this.onOperationResult = onOperationResult
    this.onStateChange = onStateChange
    this.onRepoFileChange = onRepoFileChange
    this.onPortChange = onPortChange
  }

  connect() {
    this.disposed = false
    if (!this.operation) {
      const restored = readStoredOperation(this.taskId, this.userId)
      if (restored) {
        this.createOperation(restored)
        this.setStatus("inited")
      }
    }
    this.clearConnectTimeout()
    this.clearReconnectTimer()
    const connectionId = this.connectionId + 1
    this.connectionId = connectionId
    this.closeSocket()

    const socket = new WebSocket(this.buildControlUrl())
    this.socket = socket
    this.connectTimeoutTimer = setTimeout(() => {
      if (this.socket !== socket || this.connectionId !== connectionId || this.disposed) {
        return
      }
      if (socket.readyState === WebSocket.CONNECTING) {
        this.setStatus("error")
        socket.close()
      }
    }, TaskControlClient.CONNECT_TIMEOUT_MS)

    socket.onopen = () => {
      if (this.socket !== socket || this.connectionId !== connectionId) {
        socket.close()
        return
      }
      this.clearConnectTimeout()
      this.setStatus("connected")
      this.sendOperation()
    }

    socket.onmessage = (event) => {
      if (this.socket !== socket || this.connectionId !== connectionId) {
        return
      }
      this.handleSocketMessage(event.data)
    }

    socket.onerror = () => {
      if (this.socket !== socket || this.connectionId !== connectionId) {
        return
      }
      this.setStatus("error")
    }

    socket.onclose = (event) => {
      if (this.connectionId !== connectionId) return
      this.clearConnectTimeout()
      if (this.socket === socket) {
        this.socket = null
      }

      this.failPendingCalls()
      if (event.code === 1008) {
        this.rejectOperation(event.reason || "Control access denied")
        this.setStatus("error")
        return
      } else if (this.operation) {
        this.clearOperationTimers()
        this.setOperationStatus("uncertain")
      }
      if (this.disposed) {
        this.setStatus("inited")
      } else {
        this.setStatus("error")
        this.scheduleReconnect()
      }
    }
  }

  dispose() {
    this.disposed = true
    this.clearConnectTimeout()
    this.clearReconnectTimer()
    this.connectionId += 1
    this.failPendingCalls()
    this.clearOperationTimers()
    const detached = this.operation
    this.operation = null
    detached?.resolve({ request_id: detached.operation.requestId, status: "detached" })
    this.closeSocket()
    this.setStatus("inited")
  }

  getState() {
    return { ...this.state, operation: this.operation ? structuredClone(this.operation.operation) : null }
  }

  async call<T>(
    kind: string,
    payload: Record<string, unknown>,
    timeout = TaskControlClient.DEFAULT_CALL_TIMEOUT_MS,
  ): Promise<T | null> {
    if (operationKinds.has(kind)) {
      return this.callOperation(kind as TaskControlOperationKind, payload) as Promise<T>
    }
    if (this.socket?.readyState !== WebSocket.OPEN) {
      return null
    }

    const requestId = this.createRequestId()
    const message: TaskControlCallMessage = {
      type: "call",
      kind,
      data: b64encode(JSON.stringify({
        ...payload,
        request_id: requestId,
      })),
    }

    return new Promise<T | null>((resolve) => {
      const timeoutId = setTimeout(() => {
        this.pendingCalls.delete(requestId)
        resolve(null)
      }, timeout)

      this.pendingCalls.set(requestId, {
        requestId,
        resolve: (value) => resolve((value as T | null | PromiseLike<T | null>) ?? null),
        timeoutId,
      })

      this.socket?.send(JSON.stringify(message))
    })
  }

  getFileList(path: string) {
    return this.call<{ files?: RepoFileStatus[] }>("repo_file_list", {
      path,
      glob_pattern: "*",
      include_hidden: true,
    }).then((response) => response?.files ?? null)
  }

  getFileDiff(path: string) {
    return this.call<{ diff?: string }>("repo_file_diff", {
      path,
      unified: true,
      context_lines: 20,
    }).then((response) => response?.diff ?? null)
  }

  getFileChanges() {
    return this.call<{ changes?: RepoFileChange[] }>("repo_file_changes", {})
      .then((response) => {
        if (!response) {
          return null
        }
        return response.changes ?? []
      })
  }

  getFileContent(path: string) {
    return this.call<{ content?: string }>("repo_read_file", {
      path,
      offset: 0,
      length: 1024 * 1024,
    }).then((response) => {
      if (!response || typeof response.content !== "string") {
        return null
      }
      return decodeRepoFileContent(response.content)
    })
  }

  getPortForwardList() {
    return this.call<{ ports?: PortForwardInfo[] }>("port_forward_list", {})
      .then((response) => {
        if (!response) {
          return null
        }

        return (response.ports ?? []).map((port) => ({
          ...port,
          whitelist_ips: port.whitelist_ips ?? [],
        }))
      })
  }

  restart(loadSession: boolean) {
    return this.restartOperation(loadSession).then((response) => !!response?.success)
  }

  restartOperation(loadSession: boolean) {
    return this.call<RestartTaskResponse>("restart", {
      load_session: loadSession,
    }, TaskControlClient.RESTART_TIMEOUT_MS)
  }

  switchModel(modelId: string, loadSession = true) {
    return this.call<SwitchModelResponse>("switch_model", {
      model_id: modelId,
      load_session: loadSession,
    }, TaskControlClient.RESTART_TIMEOUT_MS)
  }

  switchAgentResources(skillIds: string[], pluginIds: string[]) {
    return this.call<SwitchAgentResourcesResponse>("switch_agent_resources", {
      skill_ids: skillIds,
      plugin_ids: pluginIds,
    }, TaskControlClient.RESTART_TIMEOUT_MS)
  }

  static hasStoredOperation(taskId: string, userId?: string) {
    return readStoredOperation(taskId, userId) !== null
  }

  static clearStoredOperation(taskId: string, userId?: string) {
    const key = operationStorageKey(taskId, userId)
    if (key) {
      try { sessionStorage.removeItem(key) } catch { /* In-memory calls still work. */ }
    }
  }

  // Only use for an authoritative access/not-found error, never a timeout.
  rejectOperation(error: string) {
    if (this.operation) {
      this.finishOperation({ request_id: this.operation.operation.requestId, success: false, status: "failed", error })
    }
    TaskControlClient.clearStoredOperation(this.taskId, this.userId)
  }

  private callOperation(kind: TaskControlOperationKind, original: Record<string, unknown>): Promise<TaskControlCallResponse> {
    const payload = operationPayload(kind, original)
    if (!payload) return Promise.resolve({ status: "failed", success: false, error: "invalid_operation" })
    if (this.disposed) return Promise.resolve({ status: "detached" })
    if (this.operation) {
      if (this.operation.operation.kind === kind && JSON.stringify(this.operation.operation.payload) === JSON.stringify(payload)) {
        return this.operation.promise
      }
      return Promise.resolve({ status: "failed", success: false, error: "operation_in_progress" })
    }
    const pending = this.createOperation({ requestId: this.createRequestId(), kind, payload, status: "waiting" })
    this.persistOperation()
    this.setStatus(this.state.status)
    this.sendOperation()
    return pending.promise
  }

  private createOperation(operation: TaskControlOperation) {
    let resolve!: ActiveOperation["resolve"]
    const promise = new Promise<TaskControlCallResponse>(done => { resolve = done })
    const pending: ActiveOperation = { operation, promise, resolve, retries: 0 }
    this.operation = pending
    return pending
  }

  private persistOperation() {
    const key = operationStorageKey(this.taskId, this.userId)
    if (!key || !this.operation) return
    try {
      sessionStorage.setItem(key, JSON.stringify({ version: 1, taskId: this.taskId, userId: this.userId, ...this.operation.operation }))
    } catch { /* Private-mode/full storage must not discard an in-flight call. */ }
  }

  private setOperationStatus(status: TaskControlOperation["status"]) {
    if (!this.operation) return
    this.operation.operation = { ...this.operation.operation, status }
    this.setStatus(this.state.status)
  }

  private sendOperation() {
    const pending = this.operation
    if (!pending || this.disposed) return
    this.clearOperationTimers()
    if (this.socket?.readyState !== WebSocket.OPEN) {
      this.setOperationStatus("uncertain")
      return
    }
    const { requestId, kind, payload } = pending.operation
    try {
      this.socket.send(JSON.stringify({ type: "call", kind, data: b64encode(JSON.stringify({ ...payload, request_id: requestId })) }))
    } catch {
      this.setOperationStatus("uncertain")
      this.scheduleOperationRetry()
      return
    }
    // A UI deadline is not an operation failure. Keep the request and accept
    // its late reply; do not enqueue another wait while the server's 30s wait
    // is still outstanding.
    pending.timer = setTimeout(() => {
      if (this.operation !== pending) return
      this.setOperationStatus("uncertain")
    }, TaskControlClient.RESTART_TIMEOUT_MS)
    pending.retryTimer = setTimeout(() => {
      if (this.operation !== pending) return
      this.scheduleOperationRetry()
    }, TaskControlClient.OPERATION_RESPONSE_TIMEOUT_MS)
  }

  private scheduleOperationRetry() {
    const pending = this.operation
    if (!pending || this.disposed) return
    clearTimeout(pending.retryTimer)
    const delay = OPERATION_RETRY_MS[Math.min(pending.retries++, OPERATION_RETRY_MS.length - 1)]
    pending.retryTimer = setTimeout(() => {
      if (this.operation === pending && !this.disposed) this.sendOperation()
    }, delay)
  }

  private clearOperationTimers() {
    clearTimeout(this.operation?.timer)
    clearTimeout(this.operation?.retryTimer)
  }

  private finishOperation(response: TaskControlCallResponse) {
    const pending = this.operation
    if (!pending) return
    this.clearOperationTimers()
    this.operation = null
    TaskControlClient.clearStoredOperation(this.taskId, this.userId)
    this.setStatus(this.state.status)
    pending.resolve(response)
    if (!this.disposed) this.onOperationResult?.(structuredClone(pending.operation), response)
  }

  private handleSocketMessage(rawData: string) {
    let message: TaskControlStreamMessage
    try { message = JSON.parse(rawData) as TaskControlStreamMessage } catch { return }
    this.reconnectAttempts = 0

    switch (message.type) {
      case "call-response":
        this.handleCallResponse(message)
        break
      case "task-event":
        this.handleTaskEvent(message)
        break
      case "ping":
        break
      default:
        console.warn("TaskControlClient: unknown event type", message)
        break
    }
  }

  private handleCallResponse(message: TaskControlStreamMessage) {
    const response = this.decodePayload<unknown>(message.data)
    const responseObject = response && typeof response === "object" && !Array.isArray(response)
      ? response as TaskControlCallResponse & Record<string, unknown>
      : null

    const requestId = responseObject?.request_id
    if (!requestId) {
      console.warn("TaskControlClient: call-response missing request_id", {
        kind: message.kind,
        data: response,
        timestamp: message.timestamp,
      })
      return
    }

    if (this.operation?.operation.requestId === requestId) {
      if (responseObject?.status === "pending") {
        this.clearOperationTimers()
        this.setOperationStatus("pending")
        this.scheduleOperationRetry()
      } else if (responseObject && typeof responseObject.success === "boolean"
        && (responseObject.status === undefined || responseObject.status === (responseObject.success ? "succeeded" : "failed"))) {
        this.finishOperation({ ...responseObject, status: responseObject.success ? "succeeded" : "failed" })
      }
      // Malformed/inconsistent replies are not proof of a terminal outcome.
      return
    }

    const pendingCall = this.pendingCalls.get(requestId)
    if (!pendingCall) {
      return
    }

    clearTimeout(pendingCall.timeoutId)
    this.pendingCalls.delete(pendingCall.requestId)

    pendingCall.resolve(response)
  }

  private handleTaskEvent(message: TaskControlStreamMessage) {
    if (message.kind === "repo_file_change") {
      this.onRepoFileChange?.()
      return
    }

    if (message.kind === "port_change") {
      const payload = this.decodePayload<Record<string, unknown>>(message.data)
      this.onPortChange?.(payload?.change_type === "PORT_CHANGE_TYPE_OPENED")
      return
    }

    if (message.kind !== "repo_file_change") {
      console.log("TaskControlClient: task-event", {
        type: message.type,
        kind: message.kind,
        data: this.decodePayload(message.data),
        timestamp: message.timestamp,
      })
    }
  }

  private decodePayload<T = unknown>(data: unknown): T | null {
    if (typeof data !== "string") {
      return null
    }
    try {
      const text = b64decode(data)
      return text ? JSON.parse(text) as T : null
    } catch { return null }
  }

  private setStatus(status: TaskControlClientStatus) {
    this.state = { status, operation: this.operation?.operation ?? null }
    this.onStateChange?.(this.getState())
  }

  private failPendingCalls() {
    const pendingCalls = [...this.pendingCalls.values()]
    this.pendingCalls.clear()

    for (const pendingCall of pendingCalls) {
      clearTimeout(pendingCall.timeoutId)
      pendingCall.resolve(null)
    }
  }

  private createRequestId() {
    if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
      return crypto.randomUUID()
    }
    return `${Date.now()}-${Math.random()}`
  }

  private buildControlUrl() {
    const protocol = location.protocol === "https:" ? "wss:" : "ws:"
    return `${protocol}//${location.host}/api/v1/users/tasks/control?id=${this.taskId}`
  }

  private closeSocket() {
    this.clearConnectTimeout()
    if (!this.socket) {
      return
    }
    this.socket.close()
    this.socket = null
  }

  private scheduleReconnect() {
    if (this.disposed || this.reconnectTimer) {
      return
    }

    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null
      if (this.disposed) {
        return
      }
      this.connect()
    }, Math.min(1000 * 2 ** Math.min(this.reconnectAttempts++, 5), 30000))
  }

  private clearReconnectTimer() {
    if (!this.reconnectTimer) {
      return
    }
    clearTimeout(this.reconnectTimer)
    this.reconnectTimer = null
  }

  private clearConnectTimeout() {
    if (!this.connectTimeoutTimer) {
      return
    }
    clearTimeout(this.connectTimeoutTimer)
    this.connectTimeoutTimer = null
  }
}
