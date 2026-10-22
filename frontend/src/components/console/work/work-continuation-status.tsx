import { useEffect, useState } from "react"
import { Loader2 } from "lucide-react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { workError, workPost, workRequest } from "@/lib/work-api"

export type ContinuationStatus = { id: string; message_id: string; state: string; phase: string; error?: string; checkpoint_id?: string }

export function WorkContinuationStatus({ taskId, onPendingChange }: { taskId: string; onPendingChange: (pending: boolean) => void }) {
  const { t } = useTranslation()
  const [items, setItems] = useState<ContinuationStatus[]>([])
  const [busy, setBusy] = useState(false)
  const [choice, setChoice] = useState<ContinuationStatus | null>(null)
  const [refresh, setRefresh] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout>
    setItems([])
    const load = async () => {
      try {
        const data = await workRequest<{ items: ContinuationStatus[] }>(`/api/v1/users/tasks/${encodeURIComponent(taskId)}/continuations`, { signal: controller.signal })
        if (controller.signal.aborted) return
        setItems(data.items)
        onPendingChange(data.items.some(item => item.state === "waiting" || item.state === "blocked" || item.state === "pending"))
      } catch { /* Task polling owns permission/session errors; do not clear an existing recovery notice. */ }
      if (!controller.signal.aborted) timer = setTimeout(() => void load(), 2000)
    }
    void load()
    return () => { controller.abort(); clearTimeout(timer) }
  }, [taskId, refresh, onPendingChange])
  const action = async (item: ContinuationStatus, cancel = false, useCheckpoint = false) => {
    setBusy(true)
    try {
      await workPost(`/api/v1/users/tasks/${encodeURIComponent(taskId)}/continuations/retry`, { id: item.id, cancel, ...(useCheckpoint ? { checkpoint_id: item.checkpoint_id } : {}) })
      setChoice(null)
      setRefresh(value => value + 1)
    } catch (error) { toast.error(workError(error)) } finally { setBusy(false) }
  }
  const item = items.find(value => value.state === "blocked" || value.state === "waiting" || value.state === "pending")
  if (!item) return null
  const failed = item.state === "blocked"
  const phases: Record<string, string> = {
    queued: t("workContinuation.queued"), saving: t("workContinuation.saving"), restoring: t("workContinuation.restoring"),
    preparing: t("workContinuation.preparing"), starting: t("workContinuation.starting"),
  }
  return <div className="mb-2 rounded-xl border bg-muted/40 px-3 py-2 text-xs" role="status">
    <div className="flex items-start gap-2">
      {!failed && <Loader2 className="mt-0.5 size-3.5 shrink-0 animate-spin" />}
      <p className="min-w-0 flex-1 break-words leading-5">{failed ? item.error || t("workContinuation.failed") : phases[item.phase] || t("workContinuation.preparing")}</p>
    </div>
    {failed && <div className="mt-2 flex flex-wrap gap-2">
      <Button size="sm" variant="outline" disabled={busy} onClick={() => void action(item)}>{t("workContinuation.retry")}</Button>
      {item.checkpoint_id && item.checkpoint_id !== "00000000-0000-0000-0000-000000000000" && <Button size="sm" variant="outline" disabled={busy} onClick={() => setChoice(item)}>{t("workContinuation.chooseOlder")}</Button>}
      <Button size="sm" variant="ghost" disabled={busy} onClick={() => void action(item, true)}>{t("workContinuation.cancel")}</Button>
    </div>}
    <Dialog open={!!choice} onOpenChange={open => { if (!open && !busy) setChoice(null) }}>
      <DialogContent><DialogHeader><DialogTitle>{t("workContinuation.confirmTitle")}</DialogTitle><DialogDescription>{choice?.error}</DialogDescription></DialogHeader>
        <DialogFooter><Button variant="outline" disabled={busy} onClick={() => setChoice(null)}>{t("workContinuation.back")}</Button><Button disabled={busy} onClick={() => choice && void action(choice, false, true)}>{t("workContinuation.confirm")}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
}
