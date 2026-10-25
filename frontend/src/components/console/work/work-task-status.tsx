import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { workError, workPost, workRequest } from "@/lib/work-api"
import { TaskStatusBar, type TaskStatusBarProps } from "../task/task-status-bar"
import { isPendingContinuation, selectWorkTaskStatus, type ContinuationStatus, type StorageIssue } from "./work-task-status-state"

export function WorkTaskStatus({ taskId, messageId, onPendingChange, fallback, onCancel }: {
  taskId: string
  messageId?: string
  onPendingChange: (pending: boolean) => void
  fallback: TaskStatusBarProps | null
  onCancel: () => void
}) {
  const { t } = useTranslation()
  const [items, setItems] = useState<ContinuationStatus[]>([])
  const [issues, setIssues] = useState<StorageIssue[]>([])
  const [busy, setBusy] = useState(false)
  const [choice, setChoice] = useState<ContinuationStatus | null>(null)
  const [refresh, setRefresh] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    const timers = new Map<string, ReturnType<typeof setTimeout>>()
    const poll = <T,>(endpoint: string, receive: (items: T[]) => void, interval: number) => {
      const load = async () => {
        try {
          const data = await workRequest<{ items: T[] }>(`/api/v1/users/tasks/${encodeURIComponent(taskId)}/${endpoint}`, { signal: controller.signal })
          if (!controller.signal.aborted) receive(data.items)
        } catch { /* Keep known errors until a successful refresh; page polling handles session errors. */ }
        if (!controller.signal.aborted) timers.set(endpoint, setTimeout(() => void load(), interval))
      }
      void load()
    }
    poll<ContinuationStatus>("continuations", values => {
      setItems(values)
      onPendingChange(values.some(isPendingContinuation))
      setChoice(previous => previous && values.some(value => value.id === previous.id && value.state === "blocked" && value.checkpoint_id === previous.checkpoint_id) ? previous : null)
    }, 2000)
    poll<StorageIssue>("storage-status", setIssues, 5000)
    return () => { controller.abort(); timers.forEach(clearTimeout) }
  }, [taskId, refresh, onPendingChange])
  const runAction = async (request: () => Promise<unknown>) => {
    setBusy(true)
    try {
      await request()
      setChoice(null)
    } catch (error) { toast.error(workError(error)) } finally {
      setRefresh(value => value + 1)
      setBusy(false)
    }
  }
  const action = (item: ContinuationStatus, cancel = false, useCheckpoint = false) => runAction(() =>
    workPost(`/api/v1/users/tasks/${encodeURIComponent(taskId)}/continuations/retry`, { id: item.id, cancel, ...(useCheckpoint ? { checkpoint_id: item.checkpoint_id } : {}) }))
  const { kind, continuation: item, storage } = selectWorkTaskStatus(items, issues, messageId, fallback !== null)
  const failed = item?.state === "blocked"
  const savingBlocked = failed && item.phase === "saving" && !!storage
  const retryStorage = () => runAction(async () => {
    await workPost(`/api/v1/users/tasks/${encodeURIComponent(taskId)}/storage-retry`, { id: storage!.id })
    // Retrying storage alone leaves this accepted message blocked indefinitely.
    if (savingBlocked) await workPost(`/api/v1/users/tasks/${encodeURIComponent(taskId)}/continuations/retry`, { id: item.id, cancel: false })
    toast.success(t("workStorage.retryAccepted"))
  })
  if (kind === "idle") return null
  const phases: Record<string, string> = {
    queued: t("workContinuation.queued"), saving: t("workContinuation.saving"), restoring: t("workContinuation.restoring"),
    preparing: t("workContinuation.preparing"), starting: t("taskDetail.chat.preparingStatus"), waking: t("workContinuation.waking"),
  }
  const storageLabel = storage ? t(storage.kind === "artifacts" ? (storage.paused ? "workStorage.artifactsPaused" : "workStorage.artifactsRetrying") : (storage.paused ? "workStorage.paused" : "workStorage.retrying"), { count: storage.attempts }) : ""
  const label = kind === "storage" ? storageLabel
    : kind === "blocked" ? item?.error || t("workContinuation.failed")
    : kind === "running" ? t("taskDetail.chat.executionStatus")
    : phases[item?.phase ?? ""] || t("taskDetail.chat.preparingStatus")
  return <>
    <TaskStatusBar
      label={label}
      description={storage ? [kind === "storage" ? "" : storageLabel, storage.error].filter(Boolean).join("：") : undefined}
      executionTimeMs={fallback?.executionTimeMs}
      failed={failed || !!storage}
      actions={<>
        {storage?.paused && <Button size="xs" variant="outline" disabled={busy} onClick={() => void retryStorage()}>{t(storage.kind === "artifacts" ? "workStorage.artifactsRetry" : "workStorage.retry")}</Button>}
        {failed && !savingBlocked && <>
          <Button size="xs" variant="outline" disabled={busy} onClick={() => void action(item)}>{t("workContinuation.retry")}</Button>
          {item.checkpoint_id && item.checkpoint_id !== "00000000-0000-0000-0000-000000000000" && <Button size="xs" variant="outline" disabled={busy} onClick={() => setChoice(item)}>{t("workContinuation.chooseOlder")}</Button>}
        </>}
        {failed ? <Button size="xs" variant="destructive" disabled={busy} onClick={() => void action(item, true)}>{t("taskDetail.common.cancel")}</Button>
          : (fallback?.actions ?? (item && <Button size="xs" variant="destructive" onClick={onCancel}>{t("taskDetail.common.cancel")}</Button>))}
      </>}
    />
    <Dialog open={!!choice} onOpenChange={open => { if (!open && !busy) setChoice(null) }}>
      <DialogContent><DialogHeader><DialogTitle>{t("workContinuation.confirmTitle")}</DialogTitle><DialogDescription>{choice?.error}</DialogDescription></DialogHeader>
        <DialogFooter><Button variant="outline" disabled={busy} onClick={() => setChoice(null)}>{t("workContinuation.back")}</Button><Button disabled={busy} onClick={() => choice && void action(choice, false, true)}>{t("workContinuation.confirm")}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  </>
}
