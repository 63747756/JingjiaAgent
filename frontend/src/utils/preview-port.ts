import type { DomainVMPort } from "@/api/Api"

export function previewPortState(port: DomainVMPort) {
  if (port.error_message === "Port is not listening" ||
    (port.forward_id && port.status === "reserved" && !port.preview_url)) {
    return "notListening"
  }
  if (port.error_message || port.success === false) return "unavailable"
  if (port.preview_url && port.status !== "reserved") return "ready"
  if (!port.forward_id && !port.preview_url) return "notOpen"
  return "unavailable"
}
