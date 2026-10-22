import { useEffect, useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { Download } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { useTheme } from "@/components/theme-context";
import { artifactURL, workError, type WorkArtifact } from "@/lib/work-api";
import { MAX_PREVIEW_BYTES, MAX_TEXT_PREVIEW_BYTES, workPreviewBlob, workPreviewImage, workPreviewKind, workPreviewLink, type WorkPreviewKind } from "@/lib/work-previews";
import { WorkError, WorkLoading } from "./work-common";
import { WorkPdfPreview } from "./work-pdf-preview";
import "@/utils/markdown.css";

type PreviewSource = { kind: WorkPreviewKind; file: Blob; url: string; text: string };

export function WorkArtifactPreview({ artifact, version }: { artifact: WorkArtifact; version: string }) {
  const { t } = useTranslation();
  const { resolvedTheme } = useTheme();
  const [source, setSource] = useState<PreviewSource | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    let objectURL = "";
    setSource(null); setError("");
    void (async () => {
      try {
        const url = artifactURL(artifact.id, "preview", version);
        const response = await fetch(url, { credentials: "include", signal: controller.signal });
        if (!response.ok) {
          let message = t("workPreviews.unavailable");
          try { const body = await response.json(); message = body.message || message; } catch { /* Keep the employee-facing fallback. */ }
          throw new Error(message);
        }
        const kind = workPreviewKind(response.headers.get("Content-Type") || "");
        if (!kind) { await response.body?.cancel(); throw new Error(t("workPreviews.unsupported")); }
        const textual = kind === "text" || kind === "markdown" || kind === "html";
        const file = await workPreviewBlob(response, textual ? MAX_TEXT_PREVIEW_BYTES : MAX_PREVIEW_BYTES);
        const text = textual && kind !== "html" ? new TextDecoder("utf-8", { fatal: true }).decode(await file.arrayBuffer()) : "";
        if (controller.signal.aborted) return;
        if (kind === "image") objectURL = URL.createObjectURL(file);
        setSource({ kind, file, text, url: kind === "image" ? objectURL : url });
      } catch (cause) {
        if (!controller.signal.aborted) setError(cause instanceof RangeError ? t("workPreviews.tooLarge") : workError(cause));
      }
    })();
    return () => { controller.abort(); if (objectURL) URL.revokeObjectURL(objectURL); };
  }, [artifact.id, version, t]);

  if (error) return <WorkError message={error} />;
  if (!source) return <WorkLoading />;
  if (source.kind === "pdf") return <WorkPdfPreview file={source.file} filename={artifact.filename} downloadURL={source.url} />;
  return <div className="flex min-h-0 flex-1 flex-col gap-3">
    <div className="flex shrink-0 items-center justify-between gap-3">
      <p className="min-w-0 text-xs text-muted-foreground">{t(source.kind === "html" ? "workPreviews.htmlHint" : "workPreviews.savedVersion")}</p>
      <Button variant="outline" size="sm" asChild><a href={artifactURL(artifact.id, "download", version)} download><Download className="size-3.5" />{t("workPreviews.download")}</a></Button>
    </div>
    {source.kind === "html" ? <iframe key={source.url} title={t("workPreviews.htmlTitle", { name: artifact.filename })} src={source.url} sandbox="allow-scripts" referrerPolicy="no-referrer" className="min-h-0 w-full flex-1 rounded-lg border bg-white" /> : source.kind === "image" ? <div className="min-h-0 flex-1 overflow-auto rounded-lg border bg-muted/30 p-4"><img src={source.url} alt={t("workPreviews.imageTitle", { name: artifact.filename })} onError={() => setError(t("workPreviews.imageFailed"))} className="mx-auto h-auto max-w-full" /></div> : source.kind === "text" ? <pre className="min-h-0 flex-1 overflow-auto whitespace-pre-wrap break-words rounded-lg border bg-muted/20 p-4 font-mono text-sm leading-6">{source.text}</pre> : <div className={`min-h-0 flex-1 overflow-auto rounded-lg border p-4 sm:p-6 markdown-body ${resolvedTheme === "dark" ? "markdown-body-dark" : "markdown-body-light"}`}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} skipHtml urlTransform={(url, key) => key === "src" ? workPreviewImage(url) : workPreviewLink(url)} components={{
        a: ({ href, children }) => href ? <a href={href} target="_blank" rel="noopener noreferrer">{children}</a> : <span>{children}</span>,
        img: ({ src, alt }) => src ? <img src={src} alt={alt || ""} /> : <span className="text-muted-foreground">{alt || t("workPreviews.embeddedImage")}</span>,
      }}>{source.text}</ReactMarkdown>
    </div>}
  </div>;
}
