import { useRef, useState } from "react"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { IconCopy, IconDots, IconExternalLink, IconReload, IconShield, IconX } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"
import { openPreviewWindow, type PreviewAccessMode, type PreviewDiscoveryStatus, type TaskPreviewPort } from "@/utils/preview-port"
import { copyText } from "@/lib/clipboard"
import type { PortForwardInfo } from "./task-control-client"

type PreviewAction = "preview_apply" | "preview_close" | "preview_whitelist"
interface TaskPreviewPanelProps {
  ports: TaskPreviewPort[] | undefined
  status?: PreviewDiscoveryStatus
  error?: string
  onRefresh?: () => void
  onManage?: (kind: PreviewAction, payload: Record<string, unknown>) => Promise<PortForwardInfo | undefined>
  disabled?: boolean
  onClosePanel?: () => void
  embedded?: boolean
}

export function TaskPreviewPanel({ ports, status = "ready", error, onRefresh, onManage, disabled, onClosePanel, embedded = false }: TaskPreviewPanelProps) {
  const { t } = useTranslation()
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)
  const [editing, setEditing] = useState<number | null>(null)
  const [accessMode, setAccessMode] = useState<PreviewAccessMode>("public")
  const [whitelist, setWhitelist] = useState("")
  const run = async (action: () => Promise<unknown>) => {
    if (!onManage || busyRef.current) return
    busyRef.current = true
    setBusy(true)
    try {
      await action()
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error))
    } finally {
      busyRef.current = false
      setBusy(false)
    }
  }
  const visit = (port: TaskPreviewPort) => run(() => openPreviewWindow(
    () => window.open("about:blank", "_blank"),
    async () => {
      const preview = await onManage!("preview_apply", { port: port.port, reopen: port.status === "closed" })
      if (!preview?.access_url) throw new Error(t("taskDetail.preview.unavailable"))
      return preview.access_url
    },
    { blocked: t("taskDetail.preview.popupBlocked"), unavailable: t("taskDetail.preview.unavailable") },
  ))
  const saveAccess = (port: TaskPreviewPort, addCurrentIP = false) => run(async () => {
    await onManage!("preview_whitelist", {
      port: port.port,
      access_mode: accessMode,
      whitelist_ips: whitelist.split(/[\s,;]+/).filter(Boolean),
      add_current_ip: addCurrentIP,
    })
    setEditing(null)
  })
  const unavailable = disabled || status === "unconfigured"

  return (
    <div className={embedded ? "flex min-h-0 flex-col gap-3" : "flex h-full min-h-0 flex-col gap-3 rounded-lg border p-3"}>
      {!embedded && <div className="flex items-center justify-between gap-2">
        <span className="text-sm font-medium">{t("taskDetail.preview.title")}</span>
        <div className="flex gap-1">
          <Button variant="ghost" size="icon-sm" aria-label={t("common.refresh")} onClick={onRefresh} disabled={!onRefresh || busy}><IconReload className="size-4" /></Button>
          {onClosePanel && <Button variant="ghost" size="icon-sm" aria-label={t("common.close")} onClick={onClosePanel}><IconX className="size-4" /></Button>}
        </div>
      </div>}
      {status === "unconfigured" ? <p role="status" className="py-8 text-center text-sm text-muted-foreground">{t("taskDetail.preview.unconfigured")}</p> : <>
        {(error || status === "unknown") && <p role="status" className="break-words text-sm text-muted-foreground">{error || t("taskDetail.preview.unknown")}</p>}
        <div className="max-h-[55vh] min-h-0 space-y-2 overflow-y-auto">
          {(ports || []).map(port => {
            const closed = port.status === "closed"
            const unknown = port.discovery_state === "unknown"
            return <div key={port.port} className="rounded-lg border px-3 py-2.5">
              <div className="flex items-center justify-between gap-2">
                <div className="min-w-0 flex-1">
                  <p className="text-sm font-medium tabular-nums" aria-label={t("taskDetail.preview.portLabel", { port: port.port })}>{port.port}</p>
                  {(unknown || closed) && <p className="mt-1 text-xs text-muted-foreground">{t(unknown ? "taskDetail.preview.unknown" : "taskDetail.preview.closed")}</p>}
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  <Button size="sm" variant="outline" disabled={busy || unavailable || unknown || !onManage} onClick={() => void visit(port)}>
                    <IconExternalLink className="size-4" />{t(closed ? "taskDetail.preview.reopen" : "taskDetail.preview.open")}
                  </Button>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild><Button size="icon-sm" variant="ghost" aria-label={t("taskDetail.preview.more", { port: port.port })} disabled={busy || unavailable}><IconDots className="size-4" /></Button></DropdownMenuTrigger>
                    <DropdownMenuContent align="end" collisionPadding={8}>
                      <DropdownMenuItem disabled={!port.preview_url || closed} onSelect={() => void copyText(port.preview_url!).then(() => toast.success(t("taskDetail.preview.copied"))).catch(() => toast.error(t("taskDetail.preview.copyFailed")))}><IconCopy />{t("taskDetail.preview.copy")}</DropdownMenuItem>
                      <DropdownMenuItem disabled={!port.forward_id || !onManage} onSelect={() => { setEditing(port.port!); setAccessMode(port.access_mode || "public"); setWhitelist((port.white_list || []).join("\n")) }}><IconShield />{t("taskDetail.preview.permissions")}</DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem variant="destructive" disabled={!port.forward_id || closed || !onManage} onSelect={() => void run(() => onManage!("preview_close", { forward_id: port.forward_id }))}><IconX />{t("taskDetail.preview.disable")}</DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </div>
              </div>
              {editing === port.port && <div className="mt-3 space-y-3 border-t pt-3">
                <RadioGroup aria-label={t("taskDetail.preview.permissions")} value={accessMode} onValueChange={value => setAccessMode(value as PreviewAccessMode)} disabled={busy || unavailable}>
                  <label className="flex items-center gap-2 text-sm"><RadioGroupItem value="public" />{t("taskDetail.preview.publicAccess")}</label>
                  <label className="flex items-center gap-2 text-sm"><RadioGroupItem value="ip_allowlist" />{t("taskDetail.preview.restrictedAccess")}</label>
                </RadioGroup>
                {accessMode === "ip_allowlist" && <>
                  <p className="text-xs text-muted-foreground">{t("taskDetail.preview.whitelistHint")}</p>
                  <Textarea aria-label={t("taskDetail.preview.whitelist")} value={whitelist} onChange={event => setWhitelist(event.target.value)} rows={3} disabled={busy || unavailable} className="max-h-40 resize-none overflow-y-auto font-mono text-xs" />
                </>}
                <div className="flex flex-wrap gap-2">
                  <Button size="sm" disabled={busy || unavailable} onClick={() => void saveAccess(port)}>{t("common.save")}</Button>
                  {accessMode === "ip_allowlist" && <Button size="sm" variant="outline" disabled={busy || unavailable} onClick={() => void saveAccess(port, true)}>{t("taskDetail.preview.allowCurrent")}</Button>}
                  <Button size="sm" variant="ghost" disabled={busy} onClick={() => setEditing(null)}>{t("common.cancel")}</Button>
                </div>
              </div>}
            </div>
          })}
          {!error && status !== "unknown" && ports?.length === 0 && <p className="py-8 text-center text-sm text-muted-foreground">{t("taskDetail.preview.empty")}</p>}
          {!error && status !== "unknown" && ports === undefined && <p role="status" className="py-8 text-center text-sm text-muted-foreground">{t("taskDetail.preview.loading")}</p>}
        </div>
        {!!ports?.length && <p className="text-xs text-muted-foreground">{t("taskDetail.preview.accessHint")}</p>}
      </>}
    </div>
  )
}
