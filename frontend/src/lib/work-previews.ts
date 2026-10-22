export type WorkPreviewKind = "pdf" | "text" | "markdown" | "html" | "image";
export const MAX_TEXT_PREVIEW_BYTES = 4 << 20;
export const MAX_PREVIEW_BYTES = 32 << 20;

export function workPreviewKind(contentType: string): WorkPreviewKind | null {
  const mime = contentType.split(";", 1)[0].trim().toLowerCase();
  if (mime === "application/pdf") return "pdf";
  if (mime === "text/plain") return "text";
  if (mime === "text/markdown") return "markdown";
  if (mime === "text/html") return "html";
  return ["image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp", "image/svg+xml"].includes(mime) ? "image" : null;
}

// Markdown never automatically loads remote or application-relative resources.
export function workPreviewImage(url: string): string {
  return /^data:image\/(?:png|jpeg|gif|webp);base64,[a-z\d+/=\r\n]+$/i.test(url) ? url : "";
}

export function workPreviewLink(url: string): string {
  for (const character of url) {
    const code = character.charCodeAt(0);
    if (code <= 32 || code === 127) return "";
  }
  return /^(?:https?:\/\/|mailto:)/i.test(url) ? url : "";
}

export async function workPreviewBlob(response: Response, limit: number): Promise<Blob> {
  if (Number(response.headers.get("Content-Length")) > limit) {
    await response.body?.cancel();
    throw new RangeError("preview_too_large");
  }
  const reader = response.body?.getReader();
  if (!reader) throw new Error("preview_empty");
  const chunks: Uint8Array<ArrayBuffer>[] = [];
  let size = 0;
  try {
    for (;;) {
      const result = await reader.read();
      if (result.done) break;
      size += result.value.byteLength;
      if (size > limit) { await reader.cancel(); throw new RangeError("preview_too_large"); }
      chunks.push(new Uint8Array(result.value));
    }
  } finally { reader.releaseLock(); }
  if (!size) throw new Error("preview_empty");
  return new Blob(chunks, { type: response.headers.get("Content-Type") || "application/octet-stream" });
}
