import { useState } from "react"
import type { DomainVMPort } from "@/api/Api"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { IconCopy, IconExternalLink, IconReload, IconX } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"
import { previewPortState } from "@/utils/preview-port"
import { copyText } from "@/lib/clipboard"

type PreviewAction = "preview_apply" | "preview_close" | "preview_whitelist"
interface TaskPreviewPanelProps {
  ports: DomainVMPort[] | undefined
  error?: string
  onRefresh?: () => void
  onManage?: (kind: PreviewAction, payload: Record<string, unknown>) => Promise<void>
  disabled?: boolean
  onClosePanel?: () => void
  embedded?: boolean
}

export function TaskPreviewPanel({ ports, error, onRefresh, onManage, disabled, onClosePanel, embedded = false }: TaskPreviewPanelProps) {
  const { t } = useTranslation()
  const [portInput, setPortInput] = useState("")
  const [busy, setBusy] = useState(false)
  const [manualWhitelist, setManualWhitelist] = useState(false)
  const [newWhitelist, setNewWhitelist] = useState("")
  const [editing, setEditing] = useState<number | null>(null)
  const [whitelist, setWhitelist] = useState("")
  const run = async (kind: PreviewAction, payload: Record<string, unknown>) => {
    if (!onManage || busy) return
    setBusy(true)
    try {
      await onManage(kind, payload)
      if (kind === "preview_whitelist") setEditing(null)
      if (kind === "preview_apply") setPortInput("")
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error))
    } finally { setBusy(false) }
  }
  const parsedPort = Number(portInput)
  const validPort = /^\d+$/.test(portInput) && parsedPort >= 1 && parsedPort <= 65535
  const allowed = () => whitelist.split(/[\s,;]+/).filter(Boolean)

  return (
    <div className={embedded ? "flex min-h-0 flex-col gap-3" : "flex h-full min-h-0 flex-col gap-3 rounded-lg border p-3"}>
      {!embedded && <div className="flex items-center justify-between gap-2">
        <span className="text-sm font-medium">{t("taskDetail.preview.title")}</span>
        <div className="flex gap-1">
          <Button variant="ghost" size="icon-sm" aria-label={t("common.refresh")} onClick={onRefresh} disabled={!onRefresh || disabled}><IconReload className="size-4" /></Button>
          {onClosePanel && <Button variant="ghost" size="icon-sm" aria-label={t("common.close")} onClick={onClosePanel}><IconX className="size-4" /></Button>}
        </div>
      </div>}
      {disabled ? <p className="py-8 text-center text-sm text-muted-foreground">{t("taskDetail.preview.envNotReady")}</p> : <>
        {onManage && <div className="space-y-2"><form className="flex gap-2" onSubmit={event => { event.preventDefault(); if (validPort) void run("preview_apply", { port: parsedPort, ...(manualWhitelist ? { whitelist_ips: newWhitelist.split(/[\s,;]+/).filter(Boolean) } : {}) }) }}>
          <Input aria-label={t("taskDetail.preview.portPlaceholder")} placeholder={t("taskDetail.preview.portPlaceholder")} inputMode="numeric" value={portInput} onChange={event => setPortInput(event.target.value)} className="min-w-0" />
          <Button type="submit" variant="outline" disabled={!validPort || busy}>{t("taskDetail.preview.enable")}</Button>
        </form>
          <Button size="sm" variant="ghost" disabled={busy} className="px-0" onClick={() => setManualWhitelist(value => !value)}>{t("taskDetail.preview.manualWhitelist")}</Button>
          {manualWhitelist && <Textarea aria-label={t("taskDetail.preview.whitelist")} placeholder={t("taskDetail.preview.whitelistHint")} value={newWhitelist} onChange={event => setNewWhitelist(event.target.value)} rows={2} className="max-h-32 resize-none overflow-y-auto font-mono text-xs" />}
        </div>}
        {error && <p role="alert" className="break-words text-sm text-destructive">{error}</p>}
        <div className="max-h-[55vh] min-h-0 space-y-3 overflow-y-auto">
          {(ports || []).map(port => {
            const state = previewPortState(port)
            const ready = state === "ready"
            const closed = state === "closed"
            return <div key={port.port} className="space-y-2 rounded-lg border p-3">
              <div className="flex items-start justify-between gap-2">
                <div className="min-w-0 flex-1">
                  <p className="text-sm font-medium">{t("taskDetail.preview.portLabel", { port: port.port })}</p>
                  <p className="mt-1 break-words text-xs text-muted-foreground">{t(`taskDetail.preview.${state}`)}</p>
                  {port.error_message && state === "unavailable" && <p className="mt-1 break-words text-xs text-destructive">{port.error_message}</p>}
                </div>
                {ready && port.preview_url && <div className="flex shrink-0 gap-1">
                  <Button size="icon-sm" variant="ghost" aria-label={t("taskDetail.preview.copy")} onClick={() => void copyText(port.preview_url!).then(() => toast.success(t("taskDetail.preview.copied"))).catch(() => toast.error(t("taskDetail.preview.copyFailed")))}><IconCopy className="size-4" /></Button>
                  <Button size="sm" variant="outline" onClick={() => window.open(port.preview_url, "_blank", "noopener,noreferrer")}><IconExternalLink className="size-4" />{t("taskDetail.preview.open")}</Button>
                </div>}
              </div>
              {ready && port.preview_url && <p className="break-all text-xs text-muted-foreground">{port.preview_url}</p>}
              {onManage && <div className="flex flex-wrap gap-2">
                {port.forward_id && !closed ? <Button size="sm" variant="outline" disabled={busy} onClick={() => void run("preview_close", { forward_id: port.forward_id })}>{t("taskDetail.preview.disable")}</Button>
                  : <Button size="sm" variant="outline" disabled={busy} onClick={() => void run("preview_apply", { port: port.port })}>{t("taskDetail.preview.enable")}</Button>}
                {port.forward_id && <Button size="sm" variant="ghost" disabled={busy} onClick={() => { setEditing(editing === port.port ? null : port.port!); setWhitelist((port.white_list || []).join("\n")) }}>{t("taskDetail.preview.whitelist")}</Button>}
              </div>}
              {editing === port.port && <div className="space-y-2 border-t pt-3">
                <p className="text-xs text-muted-foreground">{t("taskDetail.preview.whitelistHint")}</p>
                <Textarea aria-label={t("taskDetail.preview.whitelist")} value={whitelist} onChange={event => setWhitelist(event.target.value)} rows={3} className="max-h-40 resize-none overflow-y-auto font-mono text-xs" />
                <div className="flex flex-wrap gap-2">
                  <Button size="sm" disabled={busy} onClick={() => void run("preview_whitelist", { port: port.port, whitelist_ips: allowed() })}>{t("common.save")}</Button>
                  <Button size="sm" variant="outline" disabled={busy} onClick={() => void run("preview_whitelist", { port: port.port, whitelist_ips: allowed(), add_current_ip: true })}>{t("taskDetail.preview.allowCurrent")}</Button>
                  <Button size="sm" variant="ghost" disabled={busy} onClick={() => setEditing(null)}>{t("common.cancel")}</Button>
                </div>
              </div>}
            </div>
          })}
          {!error && ports?.length === 0 && <p className="py-8 text-center text-sm text-muted-foreground">{t("taskDetail.preview.empty")}</p>}
        </div>
        <p className="text-xs text-muted-foreground">{t("taskDetail.preview.accessHint")}</p>
      </>}
    </div>
  )
}
