import type { DomainVMPort } from "@/api/Api"

export type PreviewDiscoveryStatus = "ready" | "unknown" | "stopped" | "unconfigured"
export type PreviewAccessMode = "public" | "ip_allowlist"
export type TaskPreviewPort = Omit<DomainVMPort, "status"> & {
  status?: string
  access_mode?: PreviewAccessMode
  discovery_state?: "ready" | "unknown"
}

export function previewPortState(port: TaskPreviewPort) {
  if (port.discovery_state === "unknown") return "unknown"
  if (String(port.status) === "closed") return "closed"
  if (port.error_message === "Port is not listening" ||
    (port.forward_id && port.status === "reserved" && !port.preview_url)) {
    return "notListening"
  }
  if (port.error_message || port.success === false) return "unavailable"
  if (port.preview_url && port.status !== "reserved") return "ready"
  if (!port.forward_id && !port.preview_url) return "notOpen"
  return "unavailable"
}

export function applyPreviewDiscovery(previous: TaskPreviewPort[] | undefined, ports: TaskPreviewPort[], status: PreviewDiscoveryStatus, scanComplete = true, uncertainPorts: number[] = []) {
  if (status === "stopped" || status === "unconfigured") return []
  if (status === "unknown" && !scanComplete) {
    return previous?.map(port => ({ ...port, discovery_state: "unknown" as const }))
  }
  if (uncertainPorts.length) {
    const listed = new Set(ports.map(port => port.port))
    const uncertain = new Set(uncertainPorts)
    const remembered = (previous || []).filter(port => port.port !== undefined && uncertain.has(port.port) && !listed.has(port.port))
    return [...ports, ...remembered.map(port => ({ ...port, discovery_state: "unknown" as const }))].sort((a, b) => (a.port || 0) - (b.port || 0))
  }
  return ports
}

// Never allocate a route after a popup has already been blocked. Keep opener
// detached while the authenticated request resolves and close failed windows.
export async function openPreviewWindow(
  createWindow: () => Pick<Window, "opener" | "location" | "close" | "closed"> | null,
  requestURL: () => Promise<string>,
  errors: { blocked: string; unavailable: string },
) {
  const popup = createWindow()
  if (!popup) throw new Error(errors.blocked)
  popup.opener = null
  try {
    const address = await requestURL()
    const url = new URL(address)
    if (url.protocol !== "http:" && url.protocol !== "https:") throw new Error(errors.unavailable)
    if (!popup.closed) popup.location.replace(url.href)
  } catch (error) {
    popup.close()
    throw error
  }
}

export function startPreviewPolling(
  refresh: () => void,
  page: Pick<Document, "visibilityState" | "addEventListener" | "removeEventListener">,
) {
  let timer: ReturnType<typeof setInterval> | undefined
  const update = () => {
    if (timer !== undefined) clearInterval(timer)
    timer = undefined
    if (page.visibilityState !== "visible") return
    refresh()
    timer = setInterval(refresh, 10000)
  }
  page.addEventListener("visibilitychange", update)
  update()
  return () => {
    if (timer !== undefined) clearInterval(timer)
    page.removeEventListener("visibilitychange", update)
  }
}
