import type { DocumentInitParameters } from "pdfjs-dist/types/src/display/api";

// PDF.js may load CMaps, fallback fonts, and decoders while rendering. All
// supporting files ship with this application and stay on the current origin.
export function workPdfOptions(
  data: Uint8Array,
  baseURL: string,
): DocumentInitParameters & { isEvalSupported: false } {
  const assets = new URL("pdfjs/", baseURL);
  return {
    data,
    cMapUrl: new URL("cmaps/", assets).href,
    cMapPacked: true,
    standardFontDataUrl: new URL("standard_fonts/", assets).href,
    wasmUrl: new URL("wasm/", assets).href,
    iccUrl: new URL("iccs/", assets).href,
    useSystemFonts: false,
    isEvalSupported: false,
    enableXfa: false,
    stopAtErrors: true,
  };
}

export function workPdfPageNumber(requested: number, total: number): number {
  return Math.max(1, Math.min(Math.trunc(requested) || 1, total || 1));
}
