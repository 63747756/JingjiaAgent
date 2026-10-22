import { useCallback, useEffect, useRef, useState } from "react";
import {
  Download,
  Eye,
  File,
  FileText,
  FileSpreadsheet,
  Image,
  Code,
  Presentation,
  ArrowUpFromLine,
  ArrowUpRight,
  MoreHorizontal,
  Loader2,
  X,
} from "lucide-react";
import { Link } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useCommonData } from "@/components/console/data-provider";
import {
  artifactURL,
  canEditArtifact,
  workError,
  workItems,
  workPost,
  workRequest,
  workSize,
  workTime,
  type WorkArtifact,
  type WorkArtifactPage,
  type ArtifactVersion,
} from "@/lib/work-api";
import { WorkEmpty, WorkError, WorkLoading } from "./work-common";
import { toast } from "sonner";
import { useTranslation } from "react-i18next";
import { WorkArtifactPreview } from "./work-artifact-preview";
import { ArtifactVersionMenu } from "./artifact-version-menu";
import { artifactNotice } from "@/lib/work-files";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

const artifactFormats: Record<string, { label: string; Icon: typeof File; style: string }> = {
  pdf: { label: "PDF", Icon: FileText, style: "bg-red-500/10 text-red-600 dark:text-red-400" },
  docx: { label: "Word", Icon: FileText, style: "bg-blue-500/10 text-blue-600 dark:text-blue-400" },
  xlsx: { label: "Excel", Icon: FileSpreadsheet, style: "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400" },
  csv: { label: "CSV", Icon: FileSpreadsheet, style: "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400" },
  pptx: { label: "PowerPoint", Icon: Presentation, style: "bg-orange-500/10 text-orange-600 dark:text-orange-400" },
  txt: { label: "TXT", Icon: FileText, style: "bg-muted text-muted-foreground" },
  md: { label: "Markdown", Icon: FileText, style: "bg-muted text-muted-foreground" },
  markdown: { label: "Markdown", Icon: FileText, style: "bg-muted text-muted-foreground" },
  html: { label: "HTML", Icon: Code, style: "bg-violet-500/10 text-violet-600 dark:text-violet-400" },
  htm: { label: "HTML", Icon: Code, style: "bg-violet-500/10 text-violet-600 dark:text-violet-400" },
  h5: { label: "H5", Icon: Code, style: "bg-violet-500/10 text-violet-600 dark:text-violet-400" },
};

export function WorkArtifacts({
  taskId,
  projectId,
  onClose,
  refreshSignal,
}: {
  taskId?: string;
  projectId?: string;
  onClose?: () => void;
  refreshSignal?: unknown;
}) {
  const { t } = useTranslation();
  const { projects } = useCommonData();
  const [items, setItems] = useState<WorkArtifact[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [selectedVersions, setSelectedVersions] = useState<
    Record<string, ArtifactVersion>
  >({});
  const [preview, setPreview] = useState<{
    artifact: WorkArtifact;
    version: string;
  } | null>(null);
  const [publish, setPublish] = useState<WorkArtifact | null>(null);
  const [publishProject, setPublishProject] = useState(projectId || "");
  const [busy, setBusy] = useState(false);
  const generation = useRef(0);
  const controller = useRef<AbortController | null>(null);
  const inFlight = useRef(false);
  const cursor = useRef("");
  const scrollRoot = useRef<HTMLDivElement>(null);
  const sentinel = useRef<HTMLDivElement>(null);
  const [hasMore, setHasMore] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState("");
  const load = useCallback(async (mode: "reset" | "more" = "reset") => {
    const reset = mode === "reset";
    if (!reset && (inFlight.current || !cursor.current)) return;
    if (reset) {
      generation.current++;
      controller.current?.abort();
      cursor.current = "";
      setItems([]);
      setSelectedVersions({});
      setHasMore(false);
      setLoading(true);
      setLoadingMore(false);
      setError("");
      if (scrollRoot.current) scrollRoot.current.scrollTop = 0;
    } else setLoadingMore(true);
    setMoreError("");
    const request = generation.current;
    const abort = new AbortController();
    controller.current = abort;
    inFlight.current = true;
    try {
      const params = new URLSearchParams({ limit: "24" });
      if (!reset) params.set("cursor", cursor.current);
      if (!taskId && projectId) params.set("project_id", projectId);
      const path = taskId ? `/api/v1/users/tasks/${encodeURIComponent(taskId)}/artifacts` : "/api/v1/users/work/artifacts";
      const data = await workRequest<WorkArtifactPage>(`${path}?${params}`, { signal: abort.signal });
      if (generation.current === request && !abort.signal.aborted) {
        const result = workItems(data);
        setItems(prev => reset ? result : [...prev, ...result.filter(item => !prev.some(old => old.id === item.id))]);
        cursor.current = data.has_more ? data.next_cursor || "" : "";
        setHasMore(!!cursor.current);
      }
    } catch (e) {
      if (generation.current === request && !abort.signal.aborted) {
        if (reset) setError(workError(e)); else setMoreError(workError(e));
      }
    } finally {
      if (generation.current === request && !abort.signal.aborted) {
        inFlight.current = false;
        setLoading(false);
        setLoadingMore(false);
      }
    }
  }, [taskId, projectId]);
  useEffect(() => {
    void load();
    return () => { generation.current++; controller.current?.abort(); inFlight.current = false; };
  }, [load, refreshSignal]);
  useEffect(() => {
    if (!hasMore || loading || loadingMore || moreError || !sentinel.current) return;
    const observer = new IntersectionObserver(entries => {
      if (entries.some(entry => entry.isIntersecting)) void load("more");
    }, { root: scrollRoot.current, rootMargin: "120px" });
    observer.observe(sentinel.current);
    return () => observer.disconnect();
  }, [hasMore, loading, loadingMore, moreError, load]);
  const publishSelected = async () => {
    if (!publish || !publishProject) return;
    setBusy(true);
    try {
      await workPost(
        `/api/v1/users/artifacts/${encodeURIComponent(publish.id)}/publish`,
        {
          project_id: publishProject,
          version_id:
            selectedVersions[publish.id]?.id || publish.current_version_id,
        },
      );
      setPublish(null);
      toast.success("已发布到项目成果，任务对话仍保持私有");
      await load();
    } catch (e) {
      toast.error(workError(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="flex h-full min-h-0 flex-col gap-4">
      <div className="flex shrink-0 items-start justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold">
            {taskId ? "任务成果" : projectId ? "项目成果" : "成果中心"}
          </h2>
          <p className="mt-1 text-xs leading-5 text-muted-foreground">
            {taskId
              ? "成果、版本和预览；发布到项目后成员可查看文件。"
              : t("workArtifacts.description")}
          </p>
        </div>
        {onClose && (
          <Button
            aria-label="关闭成果面板"
            variant="ghost"
            size="sm"
            onClick={onClose}
          >
            <X className="size-4" />
          </Button>
        )}
      </div>
      <div ref={scrollRoot} data-slot="artifact-scroll" className="min-h-0 flex-1 space-y-4 overflow-auto">
        {error && <WorkError message={error} retry={() => void load()} />}
        {loading ? (
          <WorkLoading />
        ) : !items.length && !error ? (
          <WorkEmpty
            title="暂时没有成果"
            description={
              taskId
                ? "任务生成文件后，可将交付文件保存为成果。"
                : "完成工作任务并保存成果后，会出现在这里。"
            }
          />
        ) : (
          <div
            className={
              taskId ? "space-y-3" : "grid gap-4 sm:grid-cols-2 xl:grid-cols-3"
            }
          >
            {items.map((artifact) => {
              const current = selectedVersions[artifact.id] || artifact.current_version || artifact.versions?.find(v => v.id === artifact.current_version_id);
              const versionID = current?.id || artifact.current_version_id;
              const validation =
                current?.validation_status ||
                artifact.validation_status ||
                "pending";
              const previewState =
                current?.preview_status || artifact.preview_status;
              const notice = artifactNotice(validation, previewState);
              const editable = canEditArtifact(artifact, taskId);
              const extension = artifact.filename.split(".").pop()?.toLowerCase() || "";
              const format = artifactFormats[extension] || (["png", "jpg", "jpeg", "gif", "webp", "bmp", "svg"].includes(extension) ? { label: extension.toUpperCase(), Icon: Image, style: "bg-fuchsia-500/10 text-fuchsia-600 dark:text-fuchsia-400" } : { label: extension.toUpperCase(), Icon: File, style: "bg-muted text-muted-foreground" });
              const FormatIcon = format.Icon;
              return (
                <article
                  key={artifact.id}
                  data-artifact-id={artifact.id}
                  className="@container flex h-full min-w-0 flex-col gap-3 rounded-xl border bg-card p-4"
                >
                  <div className="flex h-10 items-center gap-3">
                    <div className={`flex size-10 shrink-0 items-center justify-center rounded-lg ${format.style}`}>
                      <FormatIcon className="size-5" />
                    </div>
                    <div className="min-w-0 flex-1">
                      <h3
                        className="truncate text-sm font-medium"
                        title={artifact.title || artifact.filename}
                      >
                        {artifact.title || artifact.filename}
                      </h3>
                      <p className="mt-1 truncate text-xs text-muted-foreground">
                        {artifact.title && artifact.title !== artifact.filename ? artifact.filename : `${format.label} ${t("workArtifacts.file")}`.trim()}
                      </p>
                    </div>
                    {notice && <Tooltip><TooltipTrigger asChild><span tabIndex={0} className="shrink-0 rounded focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" aria-label={t(`workFiles.notice.${notice}`)}><Badge variant="outline" className="text-[10px]">{t(`workFiles.notice.${notice}`)}</Badge></span></TooltipTrigger>
                      <TooltipContent className="max-w-64">{t(notice === "preparing" ? "workArtifacts.preparingHint" : `workFiles.noticeHint.${notice}`)}</TooltipContent></Tooltip>}
                    {taskId && editable && projects.length > 0 && <DropdownMenu><DropdownMenuTrigger asChild>
                      <Button variant="ghost" size="icon-sm" aria-label={t("workArtifacts.more", { name: artifact.filename })} className="size-8 shrink-0"><MoreHorizontal className="size-4" /></Button>
                    </DropdownMenuTrigger><DropdownMenuContent align="end">
                      <DropdownMenuItem disabled={validation !== "passed" || busy} onSelect={() => { setPublish(artifact); setPublishProject(artifact.project_id || ""); }}><ArrowUpFromLine className="size-4" />{t("workArtifacts.publish")}</DropdownMenuItem>
                    </DropdownMenuContent></DropdownMenu>}
                  </div>
                  <div className="flex h-8 items-center justify-between gap-3">
                    <ArtifactVersionMenu artifact={artifact} selected={selectedVersions[artifact.id]}
                      onChange={(version) => setSelectedVersions(prev => ({ ...prev, [artifact.id]: version }))} />
                    <span className="min-w-0 truncate text-right text-xs text-muted-foreground">
                      {workSize(current?.size)} {workTime(current?.created_at)}
                    </span>
                  </div>
                  <div className="mt-auto flex h-11 shrink-0 items-end gap-2 border-t pt-3">
                    <Button
                      variant="outline"
                      size="sm"
                      className="px-2"
                      disabled={previewState !== "ready"}
                      onClick={() =>
                        setPreview({ artifact, version: versionID })
                      }
                    >
                      <Eye className="size-3.5" />
                      {previewState === "pending"
                        ? t("workArtifacts.preparing")
                        : previewState === "failed"
                          ? t("workArtifacts.previewFailed")
                          : previewState === "ready"
                            ? t("workArtifacts.preview")
                            : t("workArtifacts.noPreview")}
                    </Button>
                    <Button variant="outline" size="sm" className="px-2" asChild>
                      <a
                        href={artifactURL(artifact.id, "download", versionID)}
                        download
                      >
                        <Download className="size-3.5" />
                        {t("workArtifacts.download")}
                      </a>
                    </Button>
                    {!taskId && editable && <Button variant="ghost" size="sm" className="ml-auto gap-1 px-2" asChild>
                      <Link to={`/console/task/${artifact.task_id}`} aria-label={t("workArtifacts.openTask")} title={t("workArtifacts.openTask")}><span className="hidden @[17rem]:inline">{t("workArtifacts.openTask")}</span><ArrowUpRight className="size-3.5" /></Link>
                    </Button>}
                  </div>
                </article>
              );
            })}
          </div>
        )}

        {!loading && (hasMore || moreError) && <div ref={sentinel} className="flex flex-col items-center gap-2 py-3">
          {moreError && <p role="alert" className="text-sm text-destructive">{moreError}</p>}
          <Button variant="ghost" size="sm" disabled={loadingMore} onClick={() => void load("more")}>
            {loadingMore && <Loader2 className="size-4 animate-spin" />}
            {t(loadingMore ? "workArtifacts.loadingMore" : moreError ? "workArtifacts.retry" : "workArtifacts.loadMore")}
          </Button>
        </div>}
        {!loading && !hasMore && !error && items.length > 0 && <p className="py-3 text-center text-xs text-muted-foreground">{t("workArtifacts.allLoaded", { count: items.length })}</p>}
      </div>
      <Dialog
        open={!!preview}
        onOpenChange={(open) => {
          if (!open) setPreview(null);
        }}
      >
        <DialogContent className="flex h-[85dvh] max-w-[92vw] flex-col sm:max-w-5xl">
          <DialogHeader>
            <DialogTitle>{preview?.artifact.filename}</DialogTitle>
            <DialogDescription>
              预览保存的成果版本；可下载原文件继续编辑。
            </DialogDescription>
          </DialogHeader>
          {preview && (
            <WorkArtifactPreview
              artifact={preview.artifact}
              version={preview.version}
            />
          )}
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!publish}
        onOpenChange={(open) => {
          if (!open && !busy) setPublish(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>发布成果到项目</DialogTitle>
            <DialogDescription>
              项目成员将能查看选中的文件版本，原任务对话和私人上下文不共享。
            </DialogDescription>
          </DialogHeader>
          <Select value={publishProject} onValueChange={setPublishProject}>
            <SelectTrigger aria-label={t("workFiles.publishProject")} className="w-full"><SelectValue placeholder={t("workFiles.chooseProject")} /></SelectTrigger>
            <SelectContent>{projects.filter((project) => project.id && (!publish?.project_id || project.id === publish.project_id)).map((project) => <SelectItem key={project.id} value={project.id!}>{project.name}</SelectItem>)}</SelectContent>
          </Select>
          <Button
            disabled={!publishProject || busy}
            onClick={() => void publishSelected()}
          >
            {busy && <Loader2 className="size-4 animate-spin" />}发布选中版本
          </Button>
        </DialogContent>
      </Dialog>
    </section>
  );
}
