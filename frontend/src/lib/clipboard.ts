/** Copy during a user action, including intranet HTTP where Clipboard API is absent. */
export async function copyText(text: string): Promise<void> {
  if (globalThis.isSecureContext && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return
    } catch {
      // Browser permissions can reject the modern API even on HTTPS.
    }
  }

  const focused = document.activeElement instanceof HTMLElement ? document.activeElement : null
  const selection = document.getSelection()
  const ranges = selection ? Array.from({ length: selection.rangeCount }, (_, i) => selection.getRangeAt(i).cloneRange()) : []
  const inputSelection = focused instanceof HTMLInputElement || focused instanceof HTMLTextAreaElement
    ? { start: focused.selectionStart, end: focused.selectionEnd, direction: focused.selectionDirection } : null
  const field = document.createElement("textarea")
  field.value = text
  field.readOnly = true
  field.tabIndex = -1
  field.style.cssText = "position:fixed;left:-9999px;top:0;opacity:0;"
  // Keep focus inside modal dialogs that trap it, including native dialogs.
  const container = focused?.closest('dialog[open], [role="dialog"], [role="alertdialog"]') ?? document.body
  container.appendChild(field)
  try {
    field.focus({ preventScroll: true })
    field.select()
    if (!document.execCommand("copy")) throw new Error("Clipboard copy failed")
  } finally {
    field.remove()
    focused?.focus({ preventScroll: true })
    if (selection) {
      selection.removeAllRanges()
      for (const range of ranges) selection.addRange(range)
    }
    if (inputSelection?.start != null && inputSelection.end != null &&
        (focused instanceof HTMLInputElement || focused instanceof HTMLTextAreaElement)) {
      focused.setSelectionRange(inputSelection.start, inputSelection.end, inputSelection.direction ?? undefined)
    }
  }
}
