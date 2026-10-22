import { useEffect, useState } from "react"
import { AlertCircle } from "lucide-react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { workError, workPost, workRequest } from "@/lib/work-api"

type StorageIssue = { id: string; kind: string; error: string; attempts: number; paused: boolean }

export function WorkStorageStatus({ taskId }: { taskId: string }) {
  const { t } = useTranslation()
  const [items, setItems] = useState<StorageIssue[]>([])
  const [busy, setBusy] = useState(false)
  const [refresh, setRefresh] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout>
    setItems([])
    const load = async () => {
      try {
        const result = await workRequest<{ items: StorageIssue[] }>(`/api/v1/users/tasks/${encodeURIComponent(taskId)}/storage-status`, { signal: controller.signal })
        if (!controller.signal.aborted) setItems(result.items)
      } catch { /* Keep an existing failure notice; task polling handles auth errors. */ }
      if (!controller.signal.aborted) timer = setTimeout(() => void load(), 5000)
    }
    void load()
    return () => { controller.abort(); clearTimeout(timer) }
  }, [taskId, refresh])
  const issue = items.find(item => item.kind === "checkpoint") ?? items[0]
  if (!issue) return null
  const retry = async () => {
    setBusy(true)
    try {
      await workPost(`/api/v1/users/tasks/${encodeURIComponent(taskId)}/storage-retry`, { id: issue.id })
      setRefresh(value => value + 1)
      toast.success(t("workStorage.retryAccepted"))
    } catch (error) { toast.error(workError(error)) } finally { setBusy(false) }
  }
  return <div role="alert" className="mb-2 rounded-xl border border-amber-300/60 bg-amber-50/50 px-3 py-2 text-xs dark:bg-amber-950/20">
    <div className="flex items-start gap-2">
      <AlertCircle className="mt-0.5 size-4 shrink-0 text-amber-600" />
      <div className="min-w-0 flex-1 break-words leading-5">
        <p className="font-medium">{t(issue.kind === "artifacts" ? (issue.paused ? "workStorage.artifactsPaused" : "workStorage.artifactsRetrying") : (issue.paused ? "workStorage.paused" : "workStorage.retrying"), { count: issue.attempts })}</p>
        <p>{issue.error}</p>
      </div>
    </div>
    {issue.paused && <Button className="mt-2" size="sm" variant="outline" disabled={busy} onClick={() => void retry()}>{t(issue.kind === "artifacts" ? "workStorage.artifactsRetry" : "workStorage.retry")}</Button>}
  </div>
}
