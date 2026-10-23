import { useEffect, useState } from "react"
import { Link, useLocation, useNavigate } from "react-router-dom"
import { useTranslation } from "react-i18next"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { IS_OFFLINE_EDITION } from "@/utils/edition"

type Status = { enabled: boolean; initialized: boolean }
async function status(): Promise<Status> {
  const response = await fetch("/api/v1/deployment/status", { credentials: "same-origin" })
  if (!response.ok) throw new Error("status")
  const result = await response.json()
  if (result.code !== 0 || !result.data) throw new Error("status")
  return result.data
}

export function DeploymentRedirect() {
  const navigate = useNavigate()
  const location = useLocation()
  useEffect(() => {
    if (!IS_OFFLINE_EDITION || location.pathname === "/setup") return
    let cancelled = false
    status().then(value => { if (!cancelled && value.enabled && !value.initialized) navigate("/setup", { replace: true }) }).catch(() => {})
    return () => { cancelled = true }
  }, [navigate, location.pathname])
  return null
}

export default function SetupPage() {
  const { t } = useTranslation()
  const [state, setState] = useState<Status>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState("")
  const [form, setForm] = useState({ token: "", email: "", name: "", password: "" })
  useEffect(() => { status().then(setState).catch(() => setError(t("deployment.statusFailed"))) }, [t])
  async function submit(event: React.FormEvent) {
    event.preventDefault(); setBusy(true); setError("")
    try {
      const response = await fetch("/api/v1/deployment/setup", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(form), credentials: "same-origin" })
      const result = await response.json()
      if (!response.ok || result.code !== 0) throw new Error("setup")
      setForm({ token: "", email: "", name: "", password: "" })
      setState({ enabled: true, initialized: true })
    } catch { setError(t("deployment.setupFailed")) }
    finally { setBusy(false) }
  }
  return <main className="min-h-dvh bg-background px-5 py-16 text-foreground">
    <div className="mx-auto max-w-xl space-y-6 rounded-xl border p-6 shadow-sm">
      <h1 className="text-2xl font-semibold">{t("deployment.title")}</h1>
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      {!state && !error && <p>{t("deployment.loading")}</p>}
      {state && !state.initialized && <form onSubmit={submit} className="space-y-4">
        <p className="text-sm text-muted-foreground">{t("deployment.tokenHelp")}</p>
        {(["token", "name", "email", "password"] as const).map(field => <div className="space-y-2" key={field}>
          <Label htmlFor={field}>{t(`deployment.${field}`)}</Label>
          <Input id={field} required type={field === "email" ? "email" : field === "token" || field === "password" ? "password" : "text"} autoComplete={field === "password" ? "new-password" : field === "token" ? "off" : undefined} minLength={field === "password" ? 12 : undefined} maxLength={field === "password" ? 32 : field === "token" ? 64 : field === "name" ? 60 : 254} value={form[field]} onChange={event => setForm({ ...form, [field]: event.target.value })} />
        </div>)}
        <p className="text-xs text-muted-foreground">{t("deployment.passwordHelp")}</p>
        <Button disabled={busy} type="submit">{busy ? t("deployment.saving") : t("deployment.create")}</Button>
      </form>}
      {state?.initialized && <div className="space-y-5">
        <p className="text-sm text-muted-foreground">{t("deployment.complete")}</p>
        <ol className="list-decimal space-y-3 pl-5 text-sm">
          <li><Link className="text-primary underline" to="/manager/login">{t("deployment.login")}</Link></li>
          <li><Link className="text-primary underline" to="/manager/settings">{t("deployment.resources")}</Link><p className="mt-1 text-muted-foreground">{t("deployment.resourcesHelp")}</p></li>
          <li><Link className="text-primary underline" to="/manager/work-profiles">{t("deployment.assistant")}</Link></li>
          <li><Link className="text-primary underline" to="/console/work">{t("deployment.task")}</Link></li>
        </ol>
      </div>}
    </div>
  </main>
}
