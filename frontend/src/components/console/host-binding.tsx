import { copyText } from "@/lib/clipboard"
import { useCallback, useEffect, useRef, useState } from "react"
import { v4 as uuid } from "uuid"
import { Copy, Server, Terminal } from "lucide-react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Badge } from "@/components/ui/badge"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogTrigger } from "@/components/ui/alert-dialog"
import { Spinner } from "@/components/ui/spinner"
import { workRequest } from "@/lib/work-api"

export type HostScope = "users" | "teams"
export type RuntimeHost = { id: string; name: string; endpoint: string; scope: string; phase: "waiting" | "verifying" | "ready"; online: boolean; accepting: boolean; workspaces: number; command?: string; expires_at?: string; cpu_available: number; memory_available: number; running: number; issue?: string }
const path = (scope: HostScope) => `/api/v1/${scope}/hosts/enrollments`
const message = (error: unknown) => error instanceof Error ? error.message : String(error)

export function useRuntimeHosts(scope: HostScope, onChanged?: () => void) {
  const [nodes, setNodes] = useState<RuntimeHost[]>([])
  const [error, setError] = useState("")
  const changed = useRef(onChanged)
  const signature = useRef("")
  useEffect(() => { changed.current = onChanged }, [onChanged])
  const reload = useCallback(async () => {
    try {
      const next = await workRequest<RuntimeHost[]>(path(scope))
      const key = next.map(n => `${n.id}:${n.phase}:${n.online}`).join("|")
      if (signature.current && signature.current !== key) changed.current?.()
      signature.current = key
      setNodes(next); setError("")
    } catch (e) { setError(message(e)) }
  }, [scope])
  useEffect(() => {
    void reload()
    const tick = window.setInterval(() => { if (!document.hidden) void reload() }, 5000)
    return () => window.clearInterval(tick)
  }, [reload])
  return { nodes, error, reload }
}

export function HostBindingDialog({ open, onOpenChange, scope, onChanged, existing }: { open: boolean; onOpenChange: (open: boolean) => void; scope: HostScope; onChanged: () => void; existing?: RuntimeHost }) {
  const { t } = useTranslation()
  const [id, setID] = useState<string>(() => uuid())
  const [name, setName] = useState("")
  const [address, setAddress] = useState("")
  const [port, setPort] = useState("7443")
  const [result, setResult] = useState<RuntimeHost>()
  const [error, setError] = useState("")
  const [busy, setBusy] = useState(false)
  const changeRef = useRef(onChanged)
  useEffect(() => { changeRef.current = onChanged }, [onChanged])
  useEffect(() => {
    if (open) { setID(uuid()); setName(""); setAddress(""); setPort("7443"); setResult(existing); setError("") }
    else setResult(undefined)
  }, [open, existing])
  useEffect(() => {
    if (!open || !result?.id) return
    let stopped = false
    const timer = window.setInterval(async () => {
      try {
        const nodes = await workRequest<RuntimeHost[]>(path(scope))
        const current = nodes.find(n => n.id === result.id)
        if (!stopped && current) {
          setResult(previous => ({ ...current, command: previous?.command, expires_at: previous?.expires_at }))
          if (current.phase === "ready" && current.online) changeRef.current()
        }
      } catch { /* Keep command and form available for a transient connection failure. */ }
    }, 3000)
    return () => { stopped = true; window.clearInterval(timer) }
  }, [open, result?.id, scope])
  async function generate() {
    if (busy) return
    setBusy(true); setError("")
    try {
      const value = result
        ? await workRequest<RuntimeHost>(`${path(scope)}/${result.id}/command`, { method: "POST" })
        : await workRequest<RuntimeHost>(path(scope), { method: "POST", body: JSON.stringify({ id, name: name.trim(), address: address.trim(), port: Number(port) }) })
      setResult(value); changeRef.current()
    } catch (e) { setError(message(e)) } finally { setBusy(false) }
  }
  const complete = result?.phase === "ready" && result.online
  return <Dialog open={open} onOpenChange={onOpenChange}>
    <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-2xl">
      <DialogHeader><DialogTitle>{t("hostBinding.title")}</DialogTitle><DialogDescription>{t(scope === "teams" ? "hostBinding.teamHint" : "hostBinding.personalHint")}</DialogDescription></DialogHeader>
      {!result ? <form id="host-binding-form" className="space-y-4" onSubmit={e => { e.preventDefault(); void generate() }}>
        <div className="space-y-2"><Label htmlFor="host-binding-name">{t("hostBinding.name")}</Label><Input id="host-binding-name" value={name} onChange={e => setName(e.target.value)} required maxLength={128} placeholder={t("hostBinding.namePlaceholder")} /></div>
        <div className="grid grid-cols-[minmax(0,1fr)_100px] gap-3">
          <div className="space-y-2"><Label htmlFor="host-binding-address">{t("hostBinding.address")}</Label><Input id="host-binding-address" value={address} onChange={e => setAddress(e.target.value)} required placeholder="192.168.1.20" /></div>
          <div className="space-y-2"><Label htmlFor="host-binding-port">{t("hostBinding.port")}</Label><Input id="host-binding-port" type="number" min={1024} max={65535} value={port} onChange={e => setPort(e.target.value)} required /></div>
        </div>
        <p className="text-sm text-muted-foreground">{t("hostBinding.prerequisites")}</p>
      </form> : <div className="min-w-0 space-y-4">
        <div className="flex items-center gap-2 rounded-lg border p-3"><Server className="size-5 shrink-0 text-muted-foreground" /><div className="min-w-0 flex-1"><div className="truncate font-medium">{result.name}</div><div className="truncate text-xs text-muted-foreground">{result.endpoint}</div></div><Badge variant={complete ? "default" : "secondary"}>{t(`hostBinding.${complete ? "ready" : result.phase === "ready" ? "offline" : result.phase}`)}</Badge></div>
        <p className="text-sm text-muted-foreground">{t(complete ? "hostBinding.complete" : "hostBinding.runHint")}</p>
        {result.issue && <p role="status" className="text-sm text-amber-700 dark:text-amber-400">{t(`hostBinding.issues.${result.issue}`, { defaultValue: t("hostBinding.issues.unavailable") })}</p>}
        {result.command && <div className="min-w-0 rounded-lg border bg-muted/40"><div className="flex items-center justify-between border-b px-3 py-2"><span className="flex items-center gap-2 text-xs"><Terminal className="size-4" />{t("hostBinding.command")}</span><Button size="sm" variant="ghost" onClick={async () => { try { await copyText(result.command!); toast.success(t("hostBinding.copied")) } catch { setError(t("hostBinding.copyFailed")) } }}><Copy className="size-3.5" />{t("hostBinding.copy")}</Button></div><pre className="max-h-44 overflow-y-auto whitespace-pre-wrap break-all p-3 text-xs leading-6"><code>{result.command}</code></pre></div>}
        {!complete && <p className="text-xs text-muted-foreground">{t("hostBinding.expires")}{result.expires_at && ` · ${new Date(result.expires_at).toLocaleTimeString()}`}</p>}
      </div>}
      {error && <p role="alert" className="break-words text-sm text-destructive">{error}</p>}
      <DialogFooter><Button variant="outline" onClick={() => onOpenChange(false)}>{t("hostBinding.close")}</Button>{!complete && <Button type={result ? "button" : "submit"} form={result ? undefined : "host-binding-form"} disabled={busy} onClick={result ? () => void generate() : undefined}>{busy && <Spinner className="size-4" />}{t(result ? "hostBinding.regenerate" : "hostBinding.generate")}</Button>}</DialogFooter>
    </DialogContent>
  </Dialog>
}

export function RuntimeHostSummary({ node }: { node?: RuntimeHost }) {
  const { t } = useTranslation()
  if (!node) return null
  return <div className="mt-1 text-xs text-muted-foreground">{node.online ? t("hostBinding.resources", { cpu: node.cpu_available.toFixed(1), memory: (node.memory_available / 2 ** 30).toFixed(1), running: node.running }) : t(`hostBinding.issues.${node.issue || "unavailable"}`, { defaultValue: t("hostBinding.issues.unavailable") })}{!node.accepting && <span className="ml-2">{t("hostBinding.draining")}</span>}</div>
}

export function RuntimeHostControl({ node, scope, onChanged }: { node?: RuntimeHost; scope: HostScope; onChanged: () => void }) {
  const { t } = useTranslation()
  const [busy, setBusy] = useState(false)
  if (!node) return null
  return <div className="flex items-center gap-2"><Label className="hidden text-xs text-muted-foreground sm:block" htmlFor={`host-accept-${node.id}`}>{t("hostBinding.accepting")}</Label><Switch id={`host-accept-${node.id}`} aria-label={t("hostBinding.accepting")} checked={node.accepting} disabled={busy} onCheckedChange={async checked => { setBusy(true); try { await workRequest(`${path(scope)}/${node.id}`, { method: "PUT", body: JSON.stringify({ accepting: checked }) }); onChanged() } catch (e) { toast.error(message(e)) } finally { setBusy(false) } }} /></div>
}

export function PendingRuntimeHosts({ nodes, scope, onChanged, error }: { nodes: RuntimeHost[]; scope: HostScope; onChanged: () => void; error?: string }) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState<RuntimeHost>()
  return <>{error && <p role="alert" className="mb-3 text-sm text-destructive">{error}</p>}{nodes.filter(n => n.phase !== "ready").map(node => <div key={node.id} className="mb-3 flex flex-wrap items-center gap-3 rounded-lg border p-3"><Server className="size-5 text-muted-foreground" /><div className="min-w-0 flex-1"><div className="truncate text-sm font-medium">{node.name}</div><div className="break-all text-xs text-muted-foreground">{node.endpoint} · {t(`hostBinding.${node.phase}`)}</div></div><Button variant="outline" size="sm" onClick={() => setSelected(node)}>{t("hostBinding.continue")}</Button><AlertDialog><AlertDialogTrigger asChild><Button size="sm" variant="ghost">{t("hostBinding.cancel")}</Button></AlertDialogTrigger><AlertDialogContent><AlertDialogHeader><AlertDialogTitle>{t("hostBinding.cancelTitle")}</AlertDialogTitle></AlertDialogHeader><AlertDialogDescription>{t("hostBinding.cancelHint")}</AlertDialogDescription><AlertDialogFooter><AlertDialogCancel>{t("hostBinding.close")}</AlertDialogCancel><AlertDialogAction onClick={async () => { try { await workRequest(`${path(scope)}/${node.id}`, { method: "DELETE" }); onChanged() } catch (e) { toast.error(message(e)) } }}>{t("hostBinding.cancel")}</AlertDialogAction></AlertDialogFooter></AlertDialogContent></AlertDialog></div>)}<HostBindingDialog open={Boolean(selected)} onOpenChange={open => { if (!open) setSelected(undefined) }} scope={scope} onChanged={onChanged} existing={selected} /></>
}
