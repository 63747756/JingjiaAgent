import { useEffect, useRef, useState } from "react";
import { ChevronLeft, ChevronRight, Download, Loader2 } from "lucide-react";
import type { PDFDocumentProxy, RenderTask } from "pdfjs-dist";
import pdfWorkerURL from "pdfjs-dist/legacy/build/pdf.worker.min.mjs?worker&url";
import { Button } from "@/components/ui/button";
import { workPdfOptions, workPdfPageNumber } from "@/lib/work-pdf";
import { WorkError, WorkLoading } from "./work-common";

export function WorkPdfPreview({
  file,
  filename,
  downloadURL,
}: {
  file: Blob;
  filename: string;
  downloadURL: string;
}) {
  const [document, setDocument] = useState<PDFDocumentProxy | null>(null);
  const [pageNumber, setPageNumber] = useState(1);
  const [width, setWidth] = useState(640);
  const [rendering, setRendering] = useState(false);
  const [error, setError] = useState("");
  const containerRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    let disposed = false;
    let loading: ReturnType<typeof import("pdfjs-dist")["getDocument"]> | undefined;
    setDocument(null);
    setPageNumber(1);
    setError("");
    void (async () => {
      try {
        const pdfjs = await import("pdfjs-dist/legacy/build/pdf.mjs");
        const data = new Uint8Array(await file.arrayBuffer());
        if (disposed) return;
        pdfjs.GlobalWorkerOptions.workerSrc = pdfWorkerURL;
        loading = pdfjs.getDocument(workPdfOptions(data, window.location.origin + "/"));
        const result = await loading.promise;
        if (result.numPages > 200) {
          await loading.destroy();
          if (!disposed) setError("文档超过200页预览限制，请下载预览文件查看。");
          return;
        }
        if (!disposed) setDocument(result);
      } catch {
        if (!disposed) setError("无法显示此份PDF预览，请下载预览文件或原文件查看。");
      }
    })();
    return () => {
      disposed = true;
      void loading?.destroy().catch(() => undefined);
    };
  }, [file]);

  useEffect(() => {
    const element = containerRef.current;
    if (!element) return;
    const observer = new ResizeObserver(() => {
      setWidth(Math.max(240, element.clientWidth - 32));
    });
    observer.observe(element);
    return () => observer.disconnect();
  }, [document]);

  useEffect(() => {
    if (!document || !canvasRef.current) return;
    const canvas = canvasRef.current;
    let disposed = false;
    let renderTask: RenderTask | undefined;
    setRendering(true);
    setError("");
    void (async () => {
      try {
        const page = await document.getPage(pageNumber);
        if (disposed) return;
        const natural = page.getViewport({ scale: 1 });
        const scale = Math.min(width / natural.width, 12000 / Math.max(natural.width, natural.height));
        const viewport = page.getViewport({ scale });
        // Bound decoded canvas memory even for an unusually large PDF page.
        const density = Math.min(window.devicePixelRatio || 1, 2,
          Math.sqrt(16_000_000 / (viewport.width * viewport.height)));
        canvas.width = Math.max(1, Math.floor(viewport.width * density));
        canvas.height = Math.max(1, Math.floor(viewport.height * density));
        canvas.style.width = `${viewport.width}px`;
        canvas.style.height = `${viewport.height}px`;
        renderTask = page.render({
          canvas,
          viewport,
          transform: density === 1 ? undefined : [density, 0, 0, density, 0, 0],
          background: "white",
        });
        await renderTask.promise;
      } catch (e) {
        if (!disposed && (!(e instanceof Error) || e.name !== "RenderingCancelledException")) {
          setError("此页预览加载失败，请尝试其他页或下载预览文件查看。");
        }
      } finally {
        if (!disposed) setRendering(false);
      }
    })();
    return () => {
      disposed = true;
      renderTask?.cancel();
    };
  }, [document, pageNumber, width]);

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      <div className="flex shrink-0 flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-xs">
          <Button variant="outline" size="sm" aria-label="上一页"
            disabled={!document || pageNumber <= 1}
            onClick={() => setPageNumber((page) => workPdfPageNumber(page - 1, document?.numPages || 1))}>
            <ChevronLeft className="size-4" />
          </Button>
          <span aria-live="polite">{document ? `第 ${pageNumber} / ${document.numPages} 页` : "正在读取页数…"}</span>
          <Button variant="outline" size="sm" aria-label="下一页"
            disabled={!document || pageNumber >= document.numPages}
            onClick={() => setPageNumber((page) => workPdfPageNumber(page + 1, document?.numPages || 1))}>
            <ChevronRight className="size-4" />
          </Button>
          {rendering && <Loader2 className="size-3.5 animate-spin" aria-label="正在显示页面" />}
        </div>
        <Button variant="outline" size="sm" asChild>
          <a href={downloadURL} download={`${filename.replace(/\.[^.]+$/, "")}.pdf`}>
            <Download className="size-3.5" />下载PDF预览
          </a>
        </Button>
      </div>
      {error && <WorkError message={error} />}
      {!document && !error && <WorkLoading />}
      {document && (
        <div ref={containerRef} className="min-h-0 flex-1 overflow-auto rounded-lg border bg-muted/30 p-4">
          <canvas key={`${pageNumber}:${width}`} ref={canvasRef}
            role="img" aria-label={`${filename} 第 ${pageNumber} 页`}
            className="mx-auto max-w-full bg-white shadow-sm" />
        </div>
      )}
    </div>
  );
}
