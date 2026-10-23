import { ConstsTaskStatus, type DomainModel, type DomainProjectTask, type DomainVMPort } from "@/api/Api"
import { useAppRuntime } from "@/components/app-runtime-provider"
import { useBreadcrumbTask } from "@/components/console/breadcrumb-task-context"
import { useCommonData } from "@/components/console/data-provider"
import { PlanStepsBlock } from "@/components/console/task/chat-panel"
import { TaskChatInputBox, type TaskChatInputBoxHandle } from "@/components/console/task/chat-inputbox"
import { TaskControlClient, type TaskControlClientState, type TaskControlOperation, type TaskControlCallResponse } from "@/components/console/task/task-control-client"
import { applyTaskControlResult, isSuccessfulControlResult, taskControlNoticeKey } from "@/components/console/task/task-control-ui"
import { TaskMessageHandler, type TaskMessageHandlerStatus } from "@/components/console/task/task-message-handler"
import type { MessageType } from "@/components/console/task/message"
import { TaskMessageVirtualList, type TaskMessageVirtualListHandle, type TaskMessageVirtualListScrollOptions } from "@/components/console/task/task-message-virtual-list"
import { TaskPreparingView, useShouldShowPreparing } from "@/components/console/task/task-preparing-dialog"
import { TaskFileExplorer, type TaskFileExplorerHandle } from "@/components/console/task/task-file-explorer"
import { TaskPreviewPanel } from "@/components/console/task/task-preview-panel"
import { mergeContextUsage, type ContextUsage } from "@/components/console/task/context-usage"
import type { AvailableCommands, TaskPlan, TaskStreamStatus, TaskUserInput } from "@/components/console/task/task-shared"
import { TaskStreamClient, type TaskStreamClientState, type TaskStreamCloseReason, type TaskStreamConnectionState } from "@/components/console/task/task-stream-client"
import { TaskTerminalPanel } from "@/components/console/task/task-terminal-panel"
import { TaskUserInputIndex } from "@/components/console/task/task-user-input-index"
import { IS_OFFLINE_EDITION } from "@/utils/edition"
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { useDialogActionNavigation } from "@/components/ui/dialog-action-navigation"
import { CircularProgress } from "@/components/ui/circular-progress"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@/components/ui/resizable"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Spinner } from "@/components/ui/spinner"
import { useIsMobile } from "@/hooks/use-mobile"
import { cn } from "@/lib/utils"
import { formatTokens, getTaskDisplayName } from "@/utils/common"
import { apiRequest } from "@/utils/requestUtils"
import { loadTaskDetail, startTaskDetailPolling } from "@/utils/task-detail-polling"
import { IconCommand, IconDeviceDesktop, IconFile, IconReload, IconTrash, IconTerminal2, IconUpload } from "@tabler/icons-react"
import React from "react"
import { useParams } from "react-router-dom"
import { toast } from "sonner"
import { useTranslation } from "react-i18next"
import { WorkContinuationStatus } from "@/components/console/work/work-continuation-status"
import { WorkStorageStatus } from "@/components/console/work/work-storage-status"
import { WorkArtifacts } from "@/components/console/work/work-artifacts"
import { workRequest, workRequestID, type WorkTask } from "@/lib/work-api"
import { useLocation } from "react-router-dom"
import { FileCheck2, Settings2 } from "lucide-react"
import ModelSelect from "@/components/console/task/model-select"
import type { WorkModelOptions } from "@/lib/work-api"
import { NavigationItemActions, notifyNavigation, taskTarget, useNavigationActions } from "@/components/console/work/navigation-actions"

type SidePanelType = "files" | "artifacts"
type MobileToolsView = "tools" | "files"
type AskUserQuestionStatus = "pending" | "queued" | "submitting" | "completed" | "expired"

type MessageSource = "live" | "history"

export default function TaskDetailPage() {
  const { taskId } = useParams()
  const location = useLocation()
  const { setTaskName } = useBreadcrumbTask() ?? {}
  const { serverConfig } = useAppRuntime()
  const { user, models, subscription } = useCommonData()
  const { t } = useTranslation()
  const isMobile = useIsMobile()
  const { stoppingTaskId } = useNavigationActions()
  const [task, setTask] = React.useState<DomainProjectTask | null>(null)
  const [taskRefreshSignal, refreshTask] = React.useReducer((value: number) => value + 1, 0)
  React.useEffect(()=>{
    const update = (event: Event) => {
      const change = (event as CustomEvent).detail
      if (change?.kind === "task" && change.id === taskId) {
        if (change.action === "rename") setTask(task => task ? { ...task, title: change.title } : task)
        if (change.action === "stop") {
          setTask(task => task ? { ...task, status: ConstsTaskStatus.TaskStatusFinished } : task)
          refreshTask()
        }
      }
      if (change?.kind === "project" && change.action === "delete") {
        setTask(task => task && task.project_id === change.id ? { ...task, project_id: null } : task)
      }
    }
    window.addEventListener("task-library-changed",update);return()=>window.removeEventListener("task-library-changed",update);
  },[taskId]);
  const [workArtifactsMobile, setWorkArtifactsMobile] = React.useState(false)
  const [workRestorePending, setWorkRestorePending] = React.useState(false)
  React.useEffect(() => { setWorkRestorePending(false) }, [taskId])
  const isWorkTask = (task?.type as string) === "work" || !!(task as WorkTask | null)?.work
  const [activeSidePanel, setActiveSidePanel] = React.useState<SidePanelType | null>(null)
  const [terminalPanelOpen, setTerminalPanelOpen] = React.useState(false)
  const [previewDialogOpen, setPreviewDialogOpen] = React.useState(false)
  const [streamStatus, setStreamStatus] = React.useState<TaskMessageHandlerStatus>("inited")
  const [availableCommands, setAvailableCommands] = React.useState<AvailableCommands | null>(null)
  const [plan, setPlan] = React.useState<TaskPlan>({
    entries: [],
    version: 0,
  })
  const [contextUsage, setContextUsage] = React.useState<ContextUsage>({
    size: null,
    used: null,
  })
  const [sending, setSending] = React.useState(false)
  const [rawHistoryMessages, setRawHistoryMessages] = React.useState<MessageType[]>([])
  const [rawLiveMessages, setRawLiveMessages] = React.useState<MessageType[]>([])
  const [streamConnectionState, setStreamConnectionState] = React.useState<TaskStreamConnectionState>("closed")
  const [streamCloseReason, setStreamCloseReason] = React.useState<TaskStreamCloseReason | null>(null)
  const [queuedReplyIds, setQueuedReplyIds] = React.useState<string[]>([])
  const [submittingReplyIds, setSubmittingReplyIds] = React.useState<string[]>([])
  const [fileChangesCount, setFileChangesCount] = React.useState(0)
  const [fileRefreshSignal, setFileRefreshSignal] = React.useState(0)
  const [historyCursor, setHistoryCursor] = React.useState<string | null>(null)
  const historyCursorRef = React.useRef<string | null>(null)
  const [historyHasMore, setHistoryHasMore] = React.useState(true)
  const [historyLoaded, setHistoryLoaded] = React.useState(false)
  const [historyLoading, setHistoryLoading] = React.useState(false)
  const [historyCursorReady, setHistoryCursorReady] = React.useState(false)
  const [previewPorts, setPreviewPorts] = React.useState<DomainVMPort[] | undefined>(undefined)
  const [resetContextDialogOpen, setResetContextDialogOpen] = React.useState(false)
  const [restartAgentDialogOpen, setRestartAgentDialogOpen] = React.useState(false)
  const [restartAgentClearContext, setRestartAgentClearContext] = React.useState(false)
  const [publishConfirmDialogOpen, setPublishConfirmDialogOpen] = React.useState(false)
  const [mobileToolsOpen, setMobileToolsOpen] = React.useState(false)
  const [mobileToolsView, setMobileToolsView] = React.useState<MobileToolsView>("tools")
  const [chatAtBottom, setChatAtBottom] = React.useState(true)
  const [optimisticModel, setOptimisticModel] = React.useState<DomainModel | null>(null)
  const [modelSaving, setModelSaving] = React.useState(false)
  const modelSavingRef = React.useRef(false)
  const [pendingWorkspaceFilePath, setPendingWorkspaceFilePath] = React.useState<string | null>(null)
  const taskControlClientRef = React.useRef<TaskControlClient | null>(null)
  const [controlState, setControlState] = React.useState<TaskControlClientState>({ status: "inited", operation: null })
  const controlResultVersionRef = React.useRef(0)
  const controlOriginRef = React.useRef<"repair" | "reset" | "restart" | null>(null)
  const taskIdentity = `${user.id ?? ""}:${taskId ?? ""}`
  const taskIdentityRef = React.useRef(taskIdentity)
  taskIdentityRef.current = taskIdentity
  const controlBusy = !!controlState.operation
  const resetContextSubmitting = controlState.operation?.kind === "restart"
  const restartAgentSubmitting = controlState.operation?.kind === "restart"
  const controlNoticeKey = taskControlNoticeKey(controlState)
  const controlOperationDescription = React.useMemo(() => {
    const operation = controlState.operation
    if (!operation) return ""
    if (operation.kind === "switch_model") {
      const model = models.find((candidate) => candidate.id === operation.payload.model_id)
      return t("taskDetail.page.control.switchModel", { model: model?.model || operation.payload.model_id })
    }
    if (operation.kind === "switch_agent_resources") {
      return t("taskDetail.page.control.switchResources", {
        skills: Array.isArray(operation.payload.skill_ids) ? operation.payload.skill_ids.length : 0,
        plugins: Array.isArray(operation.payload.plugin_ids) ? operation.payload.plugin_ids.length : 0,
      })
    }
    return t(operation.payload.load_session === false ? "taskDetail.page.control.restartClear" : "taskDetail.page.control.restart")
  }, [controlState.operation, models, t])
  const streamClientRef = React.useRef<TaskStreamClient | null>(null)
  const historyLoadingRef = React.useRef(false)
  const historyAutoLoadAttemptedRef = React.useRef(false)
  const chatScrollRootRef = React.useRef<HTMLDivElement | null>(null)
  const historyLoadedRef = React.useRef(false)
  const chatScrollRef = React.useRef<HTMLDivElement | null>(null)
  const chatInputRef = React.useRef<TaskChatInputBoxHandle>(null)
  const resetContextDialogNavigation = useDialogActionNavigation()
  const restartAgentDialogNavigation = useDialogActionNavigation()
  const publishWebsiteDialogNavigation = useDialogActionNavigation()
  const chatContentRef = React.useRef<HTMLDivElement | null>(null)
  const taskMessageListRef = React.useRef<TaskMessageVirtualListHandle | null>(null)
  const taskFileExplorerRef = React.useRef<TaskFileExplorerHandle | null>(null)
  const autoScrollFrameRef = React.useRef<number | null>(null)
  const autoScrollLockTimeoutRef = React.useRef<ReturnType<typeof setTimeout> | null>(null)
  const autoScrollIntentLockedRef = React.useRef(false)
  const shouldAutoScrollChatRef = React.useRef(true)
  const previousLiveUserInputIdRef = React.useRef<string | null>(null)
  const previousLiveEndedCycleIdRef = React.useRef<string | null>(null)
  const previousRunningMessagesSignatureRef = React.useRef<string | null>(null)
  const pendingMobileToolActionRef = React.useRef<(() => void) | null>(null)
  const activeSidePanelRef = React.useRef<SidePanelType | null>(null)
  const previewDialogOpenRef = React.useRef(false)
  const showPreparing = useShouldShowPreparing(task) && !(isWorkTask && (workRestorePending || task?.status !== ConstsTaskStatus.TaskStatusPending))
  const taskInteractive = task?.status === ConstsTaskStatus.TaskStatusProcessing || (isWorkTask && !!task)
  const workDraftConsumed = React.useRef(false)
  React.useEffect(() => {
    const draft = (location.state as { workDraft?: string } | null)?.workDraft
    if (draft && taskInteractive && !workDraftConsumed.current) { chatInputRef.current?.setDraft(draft); workDraftConsumed.current = true }
  }, [location.state, taskInteractive])
  const canPublishWebsite = !IS_OFFLINE_EDITION
  const envid = task?.virtualmachine?.id
  const cancelledRef = React.useRef(false)
  const queuedReplyIdSet = React.useMemo(() => new Set(queuedReplyIds), [queuedReplyIds])
  const submittingReplyIdSet = React.useMemo(() => new Set(submittingReplyIds), [submittingReplyIds])
  const decorateMessages = React.useCallback((sourceMessages: MessageType[], source: MessageSource) => {
    return sourceMessages.map((message) => {
      if (message.type !== "ask_user_question") {
        return message
      }

      const askId = message.data.askId ?? ""
      const baseStatus = message.data.status
      const isCompleted = baseStatus === "completed"
      const isExpired = baseStatus === "failed" || baseStatus === "expired"

      let nextStatus: AskUserQuestionStatus = isCompleted ? "completed" : isExpired ? "expired" : "pending"
      if (!isCompleted && !isExpired) {
        if (source === "history" || streamConnectionState === "closed") {
          nextStatus = "expired"
        } else if (queuedReplyIdSet.has(askId)) {
          nextStatus = "queued"
        } else if (submittingReplyIdSet.has(askId)) {
          nextStatus = "submitting"
        }
      }

      return {
        ...message,
        data: {
          ...message.data,
          status: nextStatus,
        },
        onResponseAskUserQuestion: source === "live" && nextStatus === "pending"
          ? (nextAskId: string, answers: unknown) => {
            if (!nextAskId) {
              return "rejected"
            }

            const streamClient = streamClientRef.current
            if (!streamClient) {
              toast.error(t("taskDetail.page.askUser.connectionUnavailable"))
              return "rejected"
            }

            const result = streamClient.sendReplyQuestion(nextAskId, answers)
            if (result === "rejected") {
              toast.error(t("taskDetail.page.askUser.connectionClosed"))
            }
            return result
          }
          : undefined,
      }
    })
  }, [queuedReplyIdSet, streamConnectionState, submittingReplyIdSet, t])
  const historyMessages = React.useMemo(() => decorateMessages(rawHistoryMessages, "history"), [decorateMessages, rawHistoryMessages])
  const liveMessages = React.useMemo(() => decorateMessages(rawLiveMessages, "live"), [decorateMessages, rawLiveMessages])
  const runningMessagesSignature = React.useMemo(() => JSON.stringify(
    liveMessages
      .filter((message) => (
        message.type === "agent_message_chunk"
        || message.type === "agent_thought_chunk"
        || message.type === "tool_call"
        || message.type === "ask_user_question"
      ))
      .map((message) => ({
        id: message.id,
        type: message.type,
        content: message.data.content ?? null,
        status: message.data.status ?? null,
        title: message.data.title ?? null,
        askId: message.data.askId ?? null,
        toolCallId: message.data.toolCallId ?? null,
        questions: message.data.questions ?? null,
      })),
  ), [liveMessages])
  const latestLiveUserInputId = React.useMemo(() => {
    for (let index = liveMessages.length - 1; index >= 0; index -= 1) {
      const message = liveMessages[index]
      if (message.type === "user_input") {
        return message.id
      }
    }
    return null
  }, [liveMessages])
  const latestCompletedLiveCycleId = React.useMemo(() => {
    if (!latestLiveUserInputId) {
      return null
    }
    if (streamStatus === "finished" || streamCloseReason === "task_ended") {
      return latestLiveUserInputId
    }
    return null
  }, [latestLiveUserInputId, streamCloseReason, streamStatus])
  const [timeCost, setTimeCost] = React.useState(0)
  const previewPortCount = (previewPorts ?? []).length
  const totalTokens = task?.stats?.total_tokens ?? ((task?.stats?.input_tokens ?? 0) + (task?.stats?.output_tokens ?? 0))
  const displayedContext = mergeContextUsage(contextUsage, task?.context_usage)
  const hasContextUsage = displayedContext.used !== null && !!displayedContext.size
  const contextCapacity = displayedContext.size || task?.model?.context_limit
  const canInput = taskInteractive && !sending && !modelSaving && !controlBusy && streamStatus !== "connected" && streamStatus !== "inited"
  const canControl = canInput && controlState.status === "connected"
  const canSwitchModel = taskInteractive && !modelSaving && !controlBusy
  const planStreamStatus: TaskStreamStatus = streamStatus === "connected" ? "executing" : streamStatus
  const contextProgress = displayedContext.size && displayedContext.size > 0
    ? Math.min(Math.max((displayedContext.used ?? 0) / displayedContext.size, 0), 1)
    : 0
  const contextProgressClassName = contextProgress >= 0.8
    ? "text-danger"
    : contextProgress >= 0.6
      ? "text-warning"
      : "text-foreground"
  const contextUsagePercent = `${(contextProgress * 100).toFixed(1)}%`

  const hasSidePanel = !isMobile && activeSidePanel !== null
  const hasBottomTerminal = terminalPanelOpen
  const currentModel = optimisticModel ?? task?.model
  const currentModelId = currentModel?.id ?? ""
  const currentModelName = currentModel?.model ?? ""
  const [workModelOptions, setWorkModelOptions] = React.useState<WorkModelOptions | null>(null)
  const [workModelsLoading, setWorkModelsLoading] = React.useState(false)
  const [workModelsError, setWorkModelsError] = React.useState("")
  React.useEffect(() => {
    setWorkModelOptions(null)
    setWorkModelsError("")
    if (!isWorkTask || !taskId) return
    const controller = new AbortController()
    setWorkModelsLoading(true)
    void workRequest<WorkModelOptions>(`/api/v1/users/work/model-options?task_id=${encodeURIComponent(taskId)}`, { signal: controller.signal })
      .then(data => { if (!controller.signal.aborted) setWorkModelOptions(data) })
      .catch(error => { if (!controller.signal.aborted) setWorkModelsError(error instanceof Error ? error.message : String(error)) })
      .finally(() => { if (!controller.signal.aborted) setWorkModelsLoading(false) })
    return () => controller.abort()
  }, [taskId, isWorkTask, currentModelId])

  const toggleSidePanel = (panel: SidePanelType) => {
    setActiveSidePanel((prev) => (prev === panel ? null : panel))
  }

  const toggleTerminalPanel = () => {
    setTerminalPanelOpen((prev) => !prev)
  }

  const togglePreviewDialog = () => {
    setPreviewDialogOpen((prev) => !prev)
  }

  React.useEffect(() => {
    activeSidePanelRef.current = activeSidePanel
  }, [activeSidePanel])

  React.useEffect(() => {
    previewDialogOpenRef.current = previewDialogOpen
  }, [previewDialogOpen])

  React.useEffect(() => {
    if (!isMobile) {
      setMobileToolsView("tools")
      setMobileToolsOpen(false)
    }
  }, [isMobile])

  const disconnectStreamClient = React.useCallback(() => {
    const state = streamClientRef.current?.disconnect() ?? null
    streamClientRef.current = null
    return state
  }, [])

  const disposeTaskControlClient = React.useCallback(() => {
    const client = taskControlClientRef.current
    taskControlClientRef.current = null
    client?.dispose()
  }, [])

  const connectStreamClient = React.useCallback((mode: "attach" | "new", userInput?: TaskUserInput) => {
    if (!taskId) return Promise.resolve(false)

    return new Promise<boolean>((resolve) => {
      let settled = false
      const finish = (result: boolean) => {
        if (settled) return
        settled = true
        if (result && mode === "new") {
          notifyNavigation({ kind: "task", id: taskId, action: "activity" })
        }
        resolve(result)
      }

      const previousState = disconnectStreamClient()
      const previousMessages = previousState?.messages ?? rawLiveMessages
      if (mode === "new" && previousMessages.length > 0) {
        setRawHistoryMessages((prev) => [...prev, ...previousMessages])
        setRawLiveMessages([])
      }

      setAvailableCommands(null)
      setPlan({
        entries: [],
        version: 0,
      })
      setStreamStatus("inited")
      setStreamConnectionState("connecting")
      setStreamCloseReason(null)
      setQueuedReplyIds([])
      setSubmittingReplyIds([])
      setSending(mode === "new")
      setTimeCost(0)

      const client = mode === "attach"
        ? TaskStreamClient.attach({
          taskId,
          onStateChange: (state: TaskStreamClientState) => {
            if (streamClientRef.current !== client || cancelledRef.current) return
            setStreamStatus(state.status)
            setRawLiveMessages(state.messages)
            setAvailableCommands(state.availableCommands)
            setPlan(state.plan)
            setContextUsage((prev) => mergeContextUsage(prev, state.contextUsage))
            setTimeCost(state.executionTimeMs)
            setStreamConnectionState(state.connectionState)
            setStreamCloseReason(state.closeReason)
            setQueuedReplyIds(state.queuedReplyIds)
            setSubmittingReplyIds(state.submittingReplyIds)
            if (!historyLoadedRef.current && state.historyCursor.ready) {
              setHistoryCursorReady(true)
              setHistoryCursor(state.historyCursor.cursor)
              historyCursorRef.current = state.historyCursor.cursor
              setHistoryHasMore(state.historyCursor.hasMore)
            }
          },
          onOpen: () => {
            if (streamClientRef.current !== client || cancelledRef.current) return
            setSending(false)
            finish(true)
          },
          onClose: () => {
            if (streamClientRef.current === client) {
              streamClientRef.current = null
            }
            if (!cancelledRef.current) {
              setSending(false)
            }
            finish(false)
          },
          onError: () => {
            if (streamClientRef.current !== client || cancelledRef.current) return
            setSending(false)
            finish(false)
          },
        })
        : TaskStreamClient.new({
          taskId,
          onStateChange: (state: TaskStreamClientState) => {
            if (streamClientRef.current !== client || cancelledRef.current) return
            setStreamStatus(state.status)
            setRawLiveMessages(state.messages)
            setAvailableCommands(state.availableCommands)
            setPlan(state.plan)
            setContextUsage((prev) => mergeContextUsage(prev, state.contextUsage))
            setTimeCost(state.executionTimeMs)
            setStreamConnectionState(state.connectionState)
            setStreamCloseReason(state.closeReason)
            setQueuedReplyIds(state.queuedReplyIds)
            setSubmittingReplyIds(state.submittingReplyIds)
            if (client.hasAcceptedInput(state)) {
              setSending(false)
              finish(true)
            } else if (state.status === "error" || state.status === "finished") {
              setSending(false)
              finish(false)
            }
          },
          onOpen: () => {
            // A connected socket is not an input acceptance receipt.
          },
          onClose: () => {
            if (streamClientRef.current === client) {
              streamClientRef.current = null
            }
            if (!cancelledRef.current) {
              setSending(false)
            }
            finish(false)
          },
          onError: () => {
            if (streamClientRef.current !== client || cancelledRef.current) return
            setSending(false)
            finish(false)
          },
          userInput: userInput ?? "",
        })

      streamClientRef.current = client
      client.connect()
    })
  }, [disconnectStreamClient, rawLiveMessages, taskId])

  React.useEffect(() => {
    if (!taskId) return
    disconnectStreamClient()
    disposeTaskControlClient()
    setTask(null)
    setControlState({ status: "inited", operation: null })
    controlOriginRef.current = null
    setOptimisticModel(null)
    setModelSaving(false)
    modelSavingRef.current = false
    setResetContextDialogOpen(false)
    setRestartAgentDialogOpen(false)
    setContextUsage({ size: null, used: null })
    setActiveSidePanel(null)
    setWorkArtifactsMobile(false)
    setMobileToolsOpen(false)
    setTerminalPanelOpen(false)
    setPreviewDialogOpen(false)
    setStreamStatus("inited")
    setAvailableCommands(null)
    setPlan({
      entries: [],
      version: 0,
    })
    setSending(false)
    setRawHistoryMessages([])
    setRawLiveMessages([])
    setStreamConnectionState("closed")
    setQueuedReplyIds([])
    setSubmittingReplyIds([])
    setFileChangesCount(0)
    setFileRefreshSignal(0)
    setHistoryCursor(null)
    historyCursorRef.current = null
    setHistoryHasMore(true)
    setHistoryLoaded(false)
    setHistoryCursorReady(false)
    historyLoadedRef.current = false
    setHistoryLoading(false)
    setPreviewPorts(undefined)
    setTimeCost(0)
    historyLoadingRef.current = false
    historyAutoLoadAttemptedRef.current = false
  }, [disconnectStreamClient, disposeTaskControlClient, taskId, user.id])

  const syncFileChangesCount = React.useCallback(async () => {
    const changes = await taskControlClientRef.current?.getFileChanges()
    if (cancelledRef.current || changes === null || changes === undefined) return
    setFileChangesCount(changes.length)
  }, [])

  const applyRepoFileChange = React.useCallback(() => {
    if (cancelledRef.current) return
    setFileRefreshSignal((prev) => prev + 1)
    void syncFileChangesCount()
  }, [syncFileChangesCount])

  const fetchPortForwards = React.useCallback(async () => {
    const ports = await taskControlClientRef.current?.getPortForwardList()
    if (cancelledRef.current || ports === null || ports === undefined) return

    setPreviewPorts(ports.map((port) => ({
      port: port.port,
      status: port.status as DomainVMPort["status"],
      forward_id: port.forward_id ?? undefined,
      preview_url: port.access_url ?? undefined,
      error_message: port.error_message ?? undefined,
      success: true,
    })))
  }, [])

  const handlePortChange = React.useCallback(async (opened: boolean) => {
    await fetchPortForwards()
    if (!opened || cancelledRef.current) {
      return
    }

    const currentPanel = activeSidePanelRef.current
    const shouldOpenPreview = currentPanel === null || previewDialogOpenRef.current
    if (!shouldOpenPreview) {
      return
    }

    setPreviewDialogOpen(true)
    await fetchPortForwards()
  }, [fetchPortForwards])

  const fetchTaskRounds = React.useCallback(async (cursor?: string, limit?: number) => {
    if (!taskId || historyLoadingRef.current) return
    historyLoadingRef.current = true
    setHistoryLoading(true)
    await apiRequest(
      "v1UsersTasksRoundsList",
      {
        id: taskId,
        limit: limit ?? 1,
        ...(cursor ? { cursor } : {}),
      },
      [],
      (resp) => {
        if (cancelledRef.current) return
        if (resp.code === 0) {
          const messageHandler = new TaskMessageHandler()
          messageHandler.pushChunks(resp.data?.chunks ?? [])
          const messageState = messageHandler.finalizeCycle()
          setRawHistoryMessages((prev) => [...messageState.messages, ...prev])
          setHistoryCursorReady(true)
          {
            const nextCursor = resp.data?.next_cursor ?? null
            setHistoryCursor(nextCursor)
            historyCursorRef.current = nextCursor
          }
          setHistoryHasMore(resp.data?.has_more ?? false)
          setHistoryLoaded(true)
          historyLoadedRef.current = true
        } else {
          toast.error(resp.message || t("taskDetail.rounds.loadFailed"))
        }
      },
      () => undefined,
    )
    historyLoadingRef.current = false
    if (!cancelledRef.current) {
      setHistoryLoading(false)
    }
  }, [taskId, t])

  React.useEffect(() => () => {
    cancelledRef.current = true
    disconnectStreamClient()
  }, [disconnectStreamClient, taskId])

  React.useEffect(() => {
    if (!taskId) return
    cancelledRef.current = false
    let pollControlVersion = controlResultVersionRef.current
    const stopPolling = startTaskDetailPolling({
      successPollIntervalMs: workRestorePending ? 2000 : undefined,
      loadTask: async (signal) => {
        const version = controlResultVersionRef.current
        const result = await loadTaskDetail(taskId, signal)
        pollControlVersion = version
        return result
      },
      onTask: (nextTask) => {
        // A poll started before a completed operation cannot overwrite its result.
        if (!modelSavingRef.current && pollControlVersion === controlResultVersionRef.current) setTask(nextTask)
      },
      onFailure: (failure) => {
        if (failure.kind === "stop") {
          setWorkRestorePending(false)
          taskControlClientRef.current?.rejectOperation(failure.message || t("taskDetail.page.control.unavailable"))
          TaskControlClient.clearStoredOperation(taskId, user.id)
          disposeTaskControlClient()
          setControlState({ status: "error", operation: null })
        }
        if (failure.status === 401) {
          window.location.href = "/login"
          return
        }
        toast.error(failure.message || t("taskDetail.page.toast.fetchTaskFailed"))
      },
    })
    return () => {
      stopPolling()
    }
  }, [disconnectStreamClient, disposeTaskControlClient, taskId, user.id, t, workRestorePending, taskRefreshSignal])

  React.useEffect(() => {
    if (!setTaskName) return
    if (task) {
      setTaskName(getTaskDisplayName(task, t("taskDetail.page.task.unknownName")))
    }
    return () => setTaskName?.(null)
  }, [task, setTaskName, t])

  React.useEffect(() => {
    if (!taskId || !task) return
    if (streamStatus !== "inited") return
    if (streamClientRef.current) return
    if (!taskInteractive) return
    connectStreamClient("attach")
  }, [connectStreamClient, streamStatus, task, taskId, taskInteractive])

  React.useEffect(() => {
    if (!task) return
    if (historyLoaded || historyLoading) return
    if (historyAutoLoadAttemptedRef.current) return
    if (rawLiveMessages.length > 0) return
    if (
      task.status !== ConstsTaskStatus.TaskStatusFinished
      && task.status !== ConstsTaskStatus.TaskStatusError
    ) {
      return
    }
    // A failed request must not retrigger this effect when historyLoading
    // returns to false. A page refresh or an explicit load can retry.
    historyAutoLoadAttemptedRef.current = true
    fetchTaskRounds()
  }, [fetchTaskRounds, historyLoaded, historyLoading, rawLiveMessages.length, task])

  React.useEffect(() => {
    if (!taskInteractive || !previewDialogOpen) return
    fetchPortForwards()
  }, [fetchPortForwards, previewDialogOpen, taskInteractive])

  const handleSend = React.useCallback((content: TaskUserInput) => {
    if (!taskId || taskIdentityRef.current !== taskIdentity || cancelledRef.current || taskControlClientRef.current?.getState().operation) return Promise.resolve(false)
    return connectStreamClient("new", content)
  }, [connectStreamClient, taskId, taskIdentity])
  const handleReloadSession = React.useCallback(async () => {
    const client = taskControlClientRef.current
    if (!canControl || taskIdentityRef.current !== taskIdentity || !client || client.getState().status !== "connected" || client.getState().operation) return { status: "detached" as const }
    controlOriginRef.current = "repair"
    const response = await client.restartOperation(true)
    if (taskIdentityRef.current !== taskIdentity || taskControlClientRef.current !== client || cancelledRef.current) {
      return { status: "detached" as const }
    }
    return response
  }, [canControl, taskIdentity])
  const messages = React.useMemo(() => {
    const enhanceErrorMessage = (message: MessageType) => {
      if (message.type !== "error_message") {
        return message
      }

      return {
        ...message,
        onReloadSession: handleReloadSession,
        onUserInput: handleSend,
        controlBusy: !canControl,
      }
    }

    return [...historyMessages, ...liveMessages].map(enhanceErrorMessage)
  }, [canControl, handleReloadSession, handleSend, historyMessages, liveMessages])

  const handleCompactContext = React.useCallback(() => {
    if (!canInput) return
    handleSend("/compact")
  }, [canInput, handleSend])

  const handleRequestModelSwitch = React.useCallback(async (model: DomainModel) => {
    if (!taskId || !canSwitchModel || modelSavingRef.current || taskControlClientRef.current?.getState().operation) return
    if (!model.id) {
      toast.error(t("taskDetail.page.toast.invalidModel"))
      return
    }

    if (model.id === currentModelId || (!currentModelId && model.model === currentModelName)) {
      return
    }

    const identity = taskIdentity
    modelSavingRef.current = true
    setModelSaving(true)
    setOptimisticModel(model)
    ++controlResultVersionRef.current
    try {
      const result = await workRequest<{ model?: DomainModel }>(`/api/v1/users/tasks/${encodeURIComponent(taskId)}/model`, {
        method: "PUT",
        body: JSON.stringify({ model_id: model.id, request_id: workRequestID() }),
        signal: AbortSignal.timeout(15000),
      })
      if (taskIdentityRef.current !== identity) return
      ++controlResultVersionRef.current
      setTask(previous => previous ? { ...previous, model: { ...model, ...result.model } } : previous)
      toast.success(t("taskDetail.page.toast.modelSelectedNextTurn"))
    } catch (error) {
      if (taskIdentityRef.current !== identity) return
      ++controlResultVersionRef.current
      toast.error(error instanceof Error ? error.message : t("taskDetail.page.toast.modelSwitchFailed"))
    } finally {
      if (taskIdentityRef.current === identity) {
        setOptimisticModel(null)
        setModelSaving(false)
        modelSavingRef.current = false
      }
    }
  }, [canSwitchModel, currentModelId, currentModelName, taskId, taskIdentity, t])

  const getAvailableControlClient = React.useCallback(() => {
    const client = taskControlClientRef.current
    if (!canInput || taskIdentityRef.current !== taskIdentity || client?.getState().operation) return null
    if (!client || client.getState().status !== "connected") {
      toast.error(t("taskDetail.page.control.unavailable"))
      return null
    }
    return client
  }, [canInput, taskIdentity, t])

  const handleCancel = React.useCallback(() => {
    streamClientRef.current?.sendCancel()
  }, [])

  const handleConfirmResetContext = React.useCallback(async () => {
    const client = getAvailableControlClient()
    if (!client) return
    controlOriginRef.current = "reset"
    await client.restartOperation(false)
  }, [getAvailableControlClient])

  const handleRequestRestartAgent = React.useCallback((clearContext: boolean) => {
    if (!canControl || taskControlClientRef.current?.getState().operation) return
    setRestartAgentClearContext(clearContext)
    setRestartAgentDialogOpen(true)
  }, [canControl])

  const handleConfirmRestartAgent = React.useCallback(async () => {
    const client = getAvailableControlClient()
    if (!client) return
    controlOriginRef.current = "restart"
    await client.restartOperation(!restartAgentClearContext)
  }, [getAvailableControlClient, restartAgentClearContext])

  const handleControlOperationResult = React.useEffectEvent((operation: TaskControlOperation, response: TaskControlCallResponse) => {
    if (response.status === "detached" || response.status === "pending") return
    const origin = controlOriginRef.current
    controlOriginRef.current = null
    const success = isSuccessfulControlResult(response)
    if (success) {
      const resultVersion = ++controlResultVersionRef.current
      setTask((previous) => applyTaskControlResult(previous, operation, response, models))
      if (taskId) {
        // Recovered results may contain only IDs; refresh display metadata separately.
        const resultIdentity = taskIdentity
        void loadTaskDetail(taskId, new AbortController().signal).then((result) => {
          if (cancelledRef.current || taskIdentityRef.current !== resultIdentity || controlResultVersionRef.current !== resultVersion) return
          if (result.kind === "success") setTask(result.task)
        })
      }
    }
    if (operation.kind === "switch_model") {
      if (success) toast.success(response.message || t("taskDetail.page.toast.modelSwitched"))
      else toast.error(response.error || response.message || t("taskDetail.page.toast.modelSwitchFailed"))
    } else if (operation.kind === "switch_agent_resources") {
      if (success) {
            toast.success(response.message || t("taskDetail.chat.skillsDialog.toast.success"))
      } else {
        toast.error(response.error || response.message || t("taskDetail.chat.skillsDialog.toast.failed"))
      }
    } else {
      const clearContext = operation.payload.load_session === false
      if (success) {
        setResetContextDialogOpen(false)
        setRestartAgentDialogOpen(false)
        if (clearContext) setContextUsage((previous) => ({ ...previous, used: 0, estimated: false, updated_at: Date.now() }))
      }
      // The original repair await alone may send its one follow-up turn.
      if (origin === "repair") return
      if (success) {
        toast.success(origin === "reset"
          ? t("taskDetail.restart.reset")
          : clearContext ? t("taskDetail.page.toast.agentRestartedContextCleared") : t("taskDetail.page.toast.agentRestarted"))
      } else {
        toast.error(response.error || response.message || (origin === "reset"
          ? t("taskDetail.page.toast.resetContextFailed")
          : clearContext ? t("taskDetail.page.toast.restartAgentClearFailed") : t("taskDetail.page.toast.restartAgentFailed")))
      }
    }
  })

  const shouldConnectControl = !!taskId && (taskInteractive || controlBusy || TaskControlClient.hasStoredOperation(taskId, user.id))
  React.useEffect(() => {
    if (!taskId || !shouldConnectControl) return
    const client = new TaskControlClient({
      taskId,
      userId: user.id,
      onStateChange: (state) => {
        if (taskControlClientRef.current !== client || taskIdentityRef.current !== taskIdentity || cancelledRef.current) return
        setControlState(state)
      },
      onOperationResult: (operation, response) => {
        if (taskControlClientRef.current !== client || taskIdentityRef.current !== taskIdentity || cancelledRef.current) return
        handleControlOperationResult(operation, response)
      },
      onRepoFileChange: applyRepoFileChange,
      onPortChange: handlePortChange,
    })
    taskControlClientRef.current = client
    setControlState(client.getState())
    client.connect()
    return () => {
      if (taskControlClientRef.current === client) taskControlClientRef.current = null
      client.dispose()
    }
  }, [applyRepoFileChange, handlePortChange, shouldConnectControl, taskId, taskIdentity, user.id])

  const handleConfirmPublishWebsite = React.useCallback(() => {
    chatInputRef.current?.submitPublishWebsite(serverConfig?.region)
    setPublishConfirmDialogOpen(false)
    setPreviewDialogOpen(false)
  }, [serverConfig?.region])

  const showHistoryLoadButton = historyCursorReady && (!historyLoaded || historyHasMore)

  const getChatScrollContainer = React.useCallback(() => {
    if (chatScrollRef.current?.isConnected) {
      return chatScrollRef.current
    }

    const container = chatScrollRootRef.current?.querySelector('[data-slot="scroll-area-viewport"]') as HTMLDivElement | null
    chatScrollRef.current = container
    return container
  }, [])

  const updateChatScrollState = React.useCallback((options?: { syncAutoScroll?: boolean }) => {
    const container = getChatScrollContainer()
    if (!container) return

    const maxScrollTop = Math.max(container.scrollHeight - container.clientHeight, 0)
    const hasOverflow = maxScrollTop > 4
    const isAtBottom = !hasOverflow || maxScrollTop - container.scrollTop <= 24
    setChatAtBottom((previous) => previous === isAtBottom ? previous : isAtBottom)

    if (!hasOverflow) {
      shouldAutoScrollChatRef.current = true
      return
    }

    if (options?.syncAutoScroll && !autoScrollIntentLockedRef.current) {
      shouldAutoScrollChatRef.current = isAtBottom
    }
  }, [getChatScrollContainer])

  React.useEffect(() => {
    if (showPreparing) return

    const container = getChatScrollContainer()
    const content = chatContentRef.current
    if (!container) return

    const handleScroll = () => updateChatScrollState({ syncAutoScroll: true })
    container.addEventListener("scroll", handleScroll, { passive: true })

    const resizeObserver = new ResizeObserver(() => {
      updateChatScrollState()
    })
    resizeObserver.observe(container)
    if (content) {
      resizeObserver.observe(content)
    }

    updateChatScrollState({ syncAutoScroll: true })

    return () => {
      container.removeEventListener("scroll", handleScroll)
      resizeObserver.disconnect()
    }
  }, [getChatScrollContainer, showPreparing, updateChatScrollState])

  React.useEffect(() => {
    const frame = window.requestAnimationFrame(() => {
      updateChatScrollState()
    })
    return () => window.cancelAnimationFrame(frame)
  }, [messages, hasSidePanel, hasBottomTerminal, historyLoading, historyLoaded, showHistoryLoadButton, updateChatScrollState])

  const scrollChatToMessage = React.useCallback((messageId: string, options?: TaskMessageVirtualListScrollOptions) => {
    return taskMessageListRef.current?.scrollToMessage(messageId, options) ?? false
  }, [])

  const openWorkspaceFileLink = React.useCallback((path: string) => {
    if (!path) return

    const fileExplorer = taskFileExplorerRef.current
    if (fileExplorer) {
      void fileExplorer.openFile(path)
      return
    }

    setPendingWorkspaceFilePath(path)
    if (isMobile) {
      setMobileToolsView("files")
      setMobileToolsOpen(true)
      return
    }
    setActiveSidePanel("files")
  }, [isMobile])

  React.useEffect(() => {
    const fileExplorerVisible = isMobile
      ? mobileToolsOpen && mobileToolsView === "files"
      : activeSidePanel === "files"
    if (!fileExplorerVisible || !pendingWorkspaceFilePath || !taskFileExplorerRef.current) {
      return
    }

    const path = pendingWorkspaceFilePath
    setPendingWorkspaceFilePath(null)
    void taskFileExplorerRef.current.openFile(path)
  }, [activeSidePanel, isMobile, mobileToolsOpen, mobileToolsView, pendingWorkspaceFilePath])

  const scheduleChatScrollToBottom = React.useCallback((behavior: ScrollBehavior = "smooth", options?: { forceAutoScroll?: boolean }) => {
    const container = getChatScrollContainer()
    if (!container) return

    if (options?.forceAutoScroll) {
      shouldAutoScrollChatRef.current = true
    }

    if (behavior === "smooth") {
      autoScrollIntentLockedRef.current = true
      if (autoScrollLockTimeoutRef.current !== null) {
        clearTimeout(autoScrollLockTimeoutRef.current)
      }
      autoScrollLockTimeoutRef.current = setTimeout(() => {
        autoScrollIntentLockedRef.current = false
        autoScrollLockTimeoutRef.current = null
        updateChatScrollState()
      }, 450)
    }

    if (autoScrollFrameRef.current !== null) {
      window.cancelAnimationFrame(autoScrollFrameRef.current)
    }

    autoScrollFrameRef.current = window.requestAnimationFrame(() => {
      autoScrollFrameRef.current = null
      const nextContainer = getChatScrollContainer()
      if (!nextContainer) return
      if (taskMessageListRef.current?.scrollToBottom(behavior)) return
      nextContainer.scrollTo({ top: nextContainer.scrollHeight, behavior })
    })
  }, [getChatScrollContainer, updateChatScrollState])

  const handleScrollToBottom = React.useCallback(() => {
    scheduleChatScrollToBottom("smooth", { forceAutoScroll: true })
  }, [scheduleChatScrollToBottom])

  React.useEffect(() => {
    return () => {
      if (autoScrollFrameRef.current !== null) {
        window.cancelAnimationFrame(autoScrollFrameRef.current)
        autoScrollFrameRef.current = null
      }
      if (autoScrollLockTimeoutRef.current !== null) {
        clearTimeout(autoScrollLockTimeoutRef.current)
        autoScrollLockTimeoutRef.current = null
      }
      autoScrollIntentLockedRef.current = false
    }
  }, [getChatScrollContainer])

  React.useEffect(() => {
    if (!latestLiveUserInputId) return
    if (previousLiveUserInputIdRef.current === latestLiveUserInputId) return

    previousLiveUserInputIdRef.current = latestLiveUserInputId
    scheduleChatScrollToBottom("smooth", { forceAutoScroll: true })
  }, [latestLiveUserInputId, scheduleChatScrollToBottom])

  React.useEffect(() => {
    if (!latestCompletedLiveCycleId) return
    if (previousLiveEndedCycleIdRef.current === latestCompletedLiveCycleId) return

    previousLiveEndedCycleIdRef.current = latestCompletedLiveCycleId
    if (!shouldAutoScrollChatRef.current) return

    scheduleChatScrollToBottom("smooth")
  }, [latestCompletedLiveCycleId, scheduleChatScrollToBottom])

  React.useEffect(() => {
    if (historyLoading) return
    if (previousRunningMessagesSignatureRef.current === runningMessagesSignature) return

    previousRunningMessagesSignatureRef.current = runningMessagesSignature
    if (!shouldAutoScrollChatRef.current) return

    scheduleChatScrollToBottom("auto")
  }, [historyLoading, runningMessagesSignature, scheduleChatScrollToBottom])

  const runMobileToolAction = React.useCallback((action: () => void) => {
    pendingMobileToolActionRef.current = action
    setMobileToolsOpen(false)
  }, [])

  const handleMobileToolsOpenChange = React.useCallback((open: boolean) => {
    setMobileToolsView("tools")
    setMobileToolsOpen(open)
  }, [])

  const handleMobileToolsCloseAutoFocus = React.useCallback((event: Event) => {
    const action = pendingMobileToolActionRef.current
    if (!action) return

    event.preventDefault()
    pendingMobileToolActionRef.current = null
    action()
  }, [])

  const taskName = getTaskDisplayName(task, t("taskDetail.page.work.title"))
  const detailHeader = (
    <div className="flex min-w-0 shrink-0 items-center justify-between gap-2">
      <h1 className="min-w-0 flex-1 truncate text-sm font-medium" title={taskName}>{taskName}</h1>
      {task && <NavigationItemActions target={taskTarget(task)}/>}
      <div className="flex shrink-0 items-center gap-1">
        {isWorkTask && <Button variant="ghost" size="sm"
          aria-expanded={isMobile ? workArtifactsMobile : activeSidePanel === "artifacts"}
          className={cn("h-8 gap-1.5 px-2 text-xs sm:text-sm", (isMobile ? workArtifactsMobile : activeSidePanel === "artifacts") && "bg-accent text-primary")}
          onClick={() => isMobile ? setWorkArtifactsMobile(v => !v) : toggleSidePanel("artifacts")}>
          <FileCheck2 className="size-3.5 shrink-0" />{t("taskDetail.page.work.artifacts")}
        </Button>}
        <Popover modal open={mobileToolsOpen} onOpenChange={handleMobileToolsOpenChange}>
          <PopoverTrigger asChild>
            <Button variant="ghost" size="sm" aria-expanded={mobileToolsOpen}
              className={cn("h-8 gap-1.5 px-2 text-xs sm:text-sm", mobileToolsOpen && "bg-accent text-primary")}>
              <Settings2 className="size-3.5 shrink-0" />{t("taskDetail.page.work.advancedTools")}
            </Button>
          </PopoverTrigger>
          <PopoverContent side="bottom" align="end" sideOffset={6}
            className={cn("max-h-[65dvh] overflow-y-auto p-1.5", mobileToolsView === "tools" ? "w-64 max-w-[calc(100vw-2rem)]" : "w-[calc(100vw-2rem)] max-w-[420px]")}
            onCloseAutoFocus={handleMobileToolsCloseAutoFocus}>
            {mobileToolsView === "tools" ? <div className="flex min-h-0 flex-col">
              <div className="mb-1 space-y-1.5 border-b px-3 py-2 text-xs text-muted-foreground" data-testid="task-context-usage">
                <div className="flex items-center gap-2">
                  {hasContextUsage && <CircularProgress value={displayedContext.used ?? 0} max={displayedContext.size ?? 0} size={20} strokeWidth={3} indicatorClassName={contextProgressClassName} />}
                  <span>{hasContextUsage ? t(displayedContext.estimated ? "taskDetail.page.context.estimatedPercent" : "taskDetail.page.context.usedPercent", { percent: contextUsagePercent }) : t("taskDetail.page.context.waitingUsage")}</span>
                </div>
                {contextCapacity ? <div>{t("taskDetail.page.context.tokenDetails", { used: displayedContext.used === null ? "—" : formatTokens(displayedContext.used), size: formatTokens(contextCapacity) })}</div> : null}
                {displayedContext.estimated && <div>{t("taskDetail.page.context.estimateDescription")}</div>}
                {totalTokens > 0 && <div>{t("taskDetail.page.tokenUsage", { total: formatTokens(totalTokens) })}</div>}
              </div>
              <Button variant="ghost" className="h-11 justify-start gap-2 px-3" disabled={!taskInteractive}
                aria-label={fileChangesCount > 0 ? `${t("taskDetail.panels.files")} (${fileChangesCount})` : t("taskDetail.panels.files")}
                onClick={() => isMobile ? setMobileToolsView("files") : runMobileToolAction(() => toggleSidePanel("files"))}>
                <IconFile className="size-4" />{t("taskDetail.panels.files")}{fileChangesCount > 0 && <Badge variant="secondary">{fileChangesCount}</Badge>}
              </Button>
              {!isMobile && <Button variant="ghost" className={cn("h-11 justify-start gap-2 px-3", terminalPanelOpen && "bg-accent text-primary")} disabled={!taskInteractive}
                onClick={() => runMobileToolAction(toggleTerminalPanel)}><IconTerminal2 className="size-4" />{t("taskDetail.panels.terminal")}</Button>}
              <Button variant="ghost" className={cn("h-11 justify-start gap-2 px-3", previewDialogOpen && "bg-accent text-primary")} disabled={!taskInteractive}
                onClick={() => runMobileToolAction(togglePreviewDialog)}><IconDeviceDesktop className="size-4" />{t("taskDetail.panels.preview")}{previewPortCount > 0 && <Badge variant="secondary">{previewPortCount}</Badge>}</Button>
              <div className="my-1 border-t" />
              <Button variant="ghost" className="h-11 justify-start gap-2 px-3" disabled={!canInput} onClick={() => runMobileToolAction(handleCompactContext)}>{t("taskDetail.page.context.compactTitle")}</Button>
              <Button variant="ghost" className="h-11 justify-start gap-2 px-3" disabled={!canControl} onClick={() => runMobileToolAction(() => setResetContextDialogOpen(true))}>{t("taskDetail.page.context.resetTitle")}</Button>
              <div className="my-1 border-t" />
              <div className="px-3 py-1 text-xs text-muted-foreground">{t("taskDetail.chat.commandOptions")}</div>
              <Button variant="ghost" className="h-auto min-h-11 justify-start gap-2 whitespace-normal px-3 py-2 text-left" disabled={!canControl}
                onClick={() => runMobileToolAction(() => handleRequestRestartAgent(false))}>
                <IconReload className="size-4 shrink-0" />{t("taskDetail.chat.commands.restartAgent")}
              </Button>
              <Button variant="ghost" className="h-auto min-h-11 justify-start gap-2 whitespace-normal px-3 py-2 text-left" disabled={!canControl}
                onClick={() => runMobileToolAction(() => handleRequestRestartAgent(true))}>
                <IconTrash className="size-4 shrink-0" />{t("taskDetail.chat.commands.restartAgentClear")}
              </Button>
              {(availableCommands?.commands || []).map((command) => <Button key={command.name} variant="ghost" className="h-auto min-h-11 justify-start gap-2 whitespace-normal px-3 py-2 text-left" disabled={!canInput}
                onClick={() => runMobileToolAction(() => chatInputRef.current?.setDraft(`/${command.name}`))}>
                <IconCommand className="size-4 shrink-0" /><span className="min-w-0"><span className="block break-words">/{command.name}</span><span className="block text-xs font-normal text-muted-foreground">{command.description}</span></span>
              </Button>)}
              {canPublishWebsite && <Button variant="ghost" className="h-11 justify-start gap-2 px-3" disabled={!canInput}
                onClick={() => runMobileToolAction(() => setPublishConfirmDialogOpen(true))}><IconUpload className="size-4" />{t("taskDetail.page.dialogs.publishWebsite.button")}</Button>}
            </div> : <div className="h-[min(60dvh,520px)]">
              <TaskFileExplorer ref={taskFileExplorerRef} disabled={!taskInteractive} repository={taskControlClientRef.current}
                refreshSignal={fileRefreshSignal} onChangesCountChange={setFileChangesCount} onClosePanel={() => handleMobileToolsOpenChange(false)} envid={envid} />
            </div>}
          </PopoverContent>
        </Popover>
      </div>
    </div>
  )

  return (
    <div className="flex flex-col h-full min-h-0 gap-2">
      {detailHeader}
      {isWorkTask && <Dialog open={workArtifactsMobile} onOpenChange={setWorkArtifactsMobile}><DialogContent className="flex h-[85dvh] max-w-[95vw] flex-col"><DialogHeader><DialogTitle>{t("taskDetail.page.work.artifacts")}</DialogTitle><DialogDescription>{t("taskDetail.page.work.artifactDescription")}</DialogDescription></DialogHeader><div className="min-h-0 flex-1"><WorkArtifacts taskId={taskId} refreshSignal={fileRefreshSignal} /></div></DialogContent></Dialog>}
      {controlNoticeKey && (
        <div role="status" aria-live="polite" className="flex shrink-0 items-start gap-2 rounded-md border bg-muted/50 px-3 py-2 text-sm">
          <Spinner className="mt-0.5 size-4 shrink-0" />
          <div>
            <p className="font-medium">{controlOperationDescription}</p>
            <p>{t(controlNoticeKey)}</p>
            <p className="text-xs text-muted-foreground">{t("taskDetail.page.control.closeHint")}</p>
          </div>
        </div>
      )}
      <AlertDialog
        open={resetContextDialogOpen}
        onOpenChange={(open) => {
          setResetContextDialogOpen(open)
        }}
      >
        <AlertDialogContent onKeyDown={resetContextDialogNavigation.onKeyDown}>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("taskDetail.page.dialogs.resetContext.title")}</AlertDialogTitle>
          </AlertDialogHeader>
          <AlertDialogDescription>
            {t("taskDetail.page.dialogs.resetContext.description")}
          </AlertDialogDescription>
          {controlNoticeKey && <p role="status" className="text-sm text-muted-foreground">{t(controlNoticeKey)} {t("taskDetail.page.control.closeHint")}</p>}
          <AlertDialogFooter>
            <AlertDialogCancel ref={resetContextDialogNavigation.cancelRef}>{t(controlBusy ? "taskDetail.common.close" : "taskDetail.common.cancel")}</AlertDialogCancel>
            <Button
              ref={resetContextDialogNavigation.confirmRef}
              type="button"
              onClick={() => {
                void handleConfirmResetContext()
              }}
              disabled={!canControl}
            >
              {resetContextSubmitting && <Spinner className="mr-2 size-4" />}
              {t("taskDetail.page.dialogs.confirm")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <AlertDialog
        open={restartAgentDialogOpen}
        onOpenChange={(open) => {
          setRestartAgentDialogOpen(open)
        }}
      >
        <AlertDialogContent onKeyDown={restartAgentDialogNavigation.onKeyDown}>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {restartAgentClearContext
                ? t("taskDetail.page.dialogs.restartAgent.clearTitle")
                : t("taskDetail.page.dialogs.restartAgent.title")}
            </AlertDialogTitle>
          </AlertDialogHeader>
          <AlertDialogDescription>
            {restartAgentClearContext
              ? t("taskDetail.page.dialogs.restartAgent.clearDescription")
              : t("taskDetail.page.dialogs.restartAgent.description")}
          </AlertDialogDescription>
          {controlNoticeKey && <p role="status" className="text-sm text-muted-foreground">{t(controlNoticeKey)} {t("taskDetail.page.control.closeHint")}</p>}
          <AlertDialogFooter>
            <AlertDialogCancel ref={restartAgentDialogNavigation.cancelRef}>{t(controlBusy ? "taskDetail.common.close" : "taskDetail.common.cancel")}</AlertDialogCancel>
            <Button
              ref={restartAgentDialogNavigation.confirmRef}
              type="button"
              onClick={() => {
                void handleConfirmRestartAgent()
              }}
              disabled={!canControl}
            >
              {restartAgentSubmitting && <Spinner className="mr-2 size-4" />}
              {t("taskDetail.page.dialogs.confirm")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      {showPreparing ? (
        <TaskPreparingView task={task} />
      ) : (
        <ResizablePanelGroup orientation="vertical">
          <ResizablePanel id="top" defaultSize={hasBottomTerminal ? 75 : 100} minSize={30} className="min-h-0">
            <ResizablePanelGroup orientation="horizontal">
              <ResizablePanel id="chat" defaultSize={hasSidePanel ? 50 : 100} minSize={hasSidePanel ? 30 : 100} className="min-w-0">
                <div className={cn("flex flex-col h-full min-h-0 gap-2 flex-1 min-w-0")}>
                  <div ref={chatScrollRootRef} className="flex-1 min-h-0 min-w-0 relative">
                    <ScrollArea className="h-full [&>[data-radix-scroll-area-viewport]>div]:!block">
                      <TaskMessageVirtualList
                        ref={taskMessageListRef}
                        contentRef={chatContentRef}
                        messages={messages}
                        cli={task?.cli_name}
                        fileLinkEnvid={envid}
                        onWorkspaceFileClick={openWorkspaceFileLink}
                        getScrollContainer={getChatScrollContainer}
                        showHistoryLoadButton={showHistoryLoadButton}
                        historyLoading={historyLoading}
                        historyLoaded={historyLoaded}
                        onLoadHistory={() => fetchTaskRounds(historyCursor ?? undefined)}
                        className={cn(hasSidePanel ? "w-full" : "mx-auto max-w-[960px]")}
                      />
                    </ScrollArea>
                    <TaskUserInputIndex
                      taskId={taskId ?? null}
                      liveMessages={messages}
                      getScrollContainer={getChatScrollContainer}
                      scrollToMessage={scrollChatToMessage}
                      historyHasMore={!historyLoaded || historyHasMore}
                      loadMoreHistory={() => fetchTaskRounds(historyCursorRef.current ?? undefined, 1)}
                      isAtBottom={chatAtBottom}
                      scrollToBottom={handleScrollToBottom}
                    />
                  </div>
                  <div className={cn("shrink-0", hasSidePanel ? "w-full" : "mx-auto max-w-[960px] w-full")}>
                    {taskInteractive && plan.entries.length > 0 && (
                      <div className="mb-2">
                        <PlanStepsBlock plan={plan} streamStatus={planStreamStatus} />
                      </div>
                    )}
                    {isWorkTask && taskId && <WorkStorageStatus taskId={taskId} />}
                    {isWorkTask && taskId && <WorkContinuationStatus taskId={taskId} onPendingChange={setWorkRestorePending} />}
                    {taskInteractive ? (
                      <TaskChatInputBox
                        ref={chatInputRef}
                        taskId={taskId ?? ""}
                        projectId={task?.project_id || undefined}
                        workFilesEnabled={isWorkTask}
                        hideVoice={isWorkTask}
                        renderModelSelector={() => <ModelSelect compact
                          models={isWorkTask ? workModelOptions?.items || [] : models}
                          selectedModel={currentModel} selectedModelId={currentModelId}
                          setSelectedModelId={id => { const model = (isWorkTask ? workModelOptions?.items || [] : models).find(item => item.id === id); if (model) void handleRequestModelSwitch(model) }}
                          subscription={subscription} disabled={!canSwitchModel}
                          loading={isWorkTask && workModelsLoading} error={workModelsError} />}
                        streamStatus={streamStatus}
                        onSend={handleSend}
                        onCancel={handleCancel}
                        whiteboardPersistenceKey={`task-whiteboard-${taskId}`}
                        sending={sending || modelSaving}
                        controlBusy={controlBusy || stoppingTaskId === taskId}
                        queueSize={0}
                        executionTimeMs={timeCost}
                      />
                    ) : (
                      <div className="flex items-center justify-center w-full border bg-muted/50 rounded-md p-2 text-xs text-muted-foreground">
                        {t("taskDetail.page.task.ended")}
                      </div>
                    )}
                  </div>
                </div>
              </ResizablePanel>
              {hasSidePanel && (
                <>
                  <ResizableHandle withHandle className="ml-2 shrink-0 bg-transparent after:hidden" />
                  <ResizablePanel id="right-panel" defaultSize={50} minSize={25} className="min-w-0">
                    <div className="h-full overflow-hidden flex flex-col">
                      {activeSidePanel === "artifacts" && <div className="min-h-0 flex-1 rounded-xl border bg-card p-4"><WorkArtifacts taskId={taskId} refreshSignal={fileRefreshSignal} onClose={() => setActiveSidePanel(null)} /></div>}
                      {activeSidePanel === "files" && (
                        <div className="flex-1 min-h-0 overflow-hidden">
                          <TaskFileExplorer
                            ref={taskFileExplorerRef}
                            disabled={!taskInteractive}
                            repository={taskControlClientRef.current}
                            refreshSignal={fileRefreshSignal}
                            onChangesCountChange={setFileChangesCount}
                            onClosePanel={() => setActiveSidePanel(null)}
                            envid={envid}
                          />
                        </div>
                      )}
                    </div>
                  </ResizablePanel>
                </>
              )}
            </ResizablePanelGroup>
          </ResizablePanel>
          {hasBottomTerminal && (
            <>
              <ResizableHandle withHandle className="mt-2 shrink-0 bg-transparent after:hidden" />
              <ResizablePanel id="bottom-terminal" defaultSize={25} minSize={20} className="min-h-0">
                <div className="h-full w-full border rounded-md overflow-hidden">
                  <TaskTerminalPanel envid={envid} disabled={!taskInteractive} onClosePanel={() => setTerminalPanelOpen(false)} />
                </div>
              </ResizablePanel>
            </>
          )}
        </ResizablePanelGroup>
      )}
      <Dialog open={previewDialogOpen} onOpenChange={setPreviewDialogOpen}>
        <DialogContent>
          <DialogHeader className="flex-row items-center justify-start gap-2 pr-8">
            <DialogTitle>{t("taskDetail.panels.preview")}</DialogTitle>
            <Button
              variant="ghost"
              size="icon-sm"
              className="shrink-0"
              onClick={() => void fetchPortForwards()}
              disabled={!taskInteractive}
            >
              <IconReload className="size-4" />
            </Button>
          </DialogHeader>
          <TaskPreviewPanel
            ports={previewPorts}
            onRefresh={fetchPortForwards}
            disabled={!taskInteractive}
            embedded
          />
        </DialogContent>
      </Dialog>
      {canPublishWebsite && (
        <Dialog open={publishConfirmDialogOpen} onOpenChange={setPublishConfirmDialogOpen}>
          <DialogContent onKeyDown={publishWebsiteDialogNavigation.onKeyDown}>
            <DialogHeader>
              <DialogTitle>{t("taskDetail.page.dialogs.publishWebsite.title")}</DialogTitle>
              <DialogDescription>
                {t("taskDetail.page.dialogs.publishWebsite.description")}
              </DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button ref={publishWebsiteDialogNavigation.cancelRef} variant="outline" onClick={() => setPublishConfirmDialogOpen(false)}>
                {t("taskDetail.common.cancel")}
              </Button>
              <Button ref={publishWebsiteDialogNavigation.confirmRef} onClick={handleConfirmPublishWebsite}>
                {t("taskDetail.page.dialogs.publishWebsite.confirm")}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}

    </div>
  )
}
