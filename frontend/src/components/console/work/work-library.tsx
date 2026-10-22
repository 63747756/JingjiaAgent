import { useCallback, useEffect, useRef, useState } from "react";
import { FileText, Upload, RefreshCw, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { uploadFileWithPresignedUrl } from "@/utils/common";
import {
  workError,
  workItems,
  workPost,
  workRequest,
  workSize,
  workTime,
  type WorkResource,
} from "@/lib/work-api";
import { WorkEmpty, WorkError, WorkLoading } from "./work-common";
import { FileVersionMenu } from "./file-version-menu";
import { resourceVersions } from "@/lib/work-files";

export function WorkLibrary({
  projectId,
  canWrite = true,
}: {
  projectId: string;
  canWrite?: boolean;
}) {
  const { t } = useTranslation();
  const [items, setItems] = useState<WorkResource[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [uploading, setUploading] = useState(false);
  const [selectedVersions, setSelectedVersions] = useState<
    Record<string, string>
  >({});
  const [updatingResource, setUpdatingResource] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const generation = useRef(0);
  const load = useCallback(async () => {
    const request = ++generation.current;
    setLoading(true);
    setError("");
    try {
      const data = await workRequest<{ items: WorkResource[] }>(
        `/api/v1/users/projects/${encodeURIComponent(projectId)}/resources`,
      );
      if (generation.current === request) setItems(workItems(data));
    } catch (e) {
      if (generation.current === request) setError(workError(e));
    } finally {
      if (generation.current === request) setLoading(false);
    }
  }, [projectId]);
  useEffect(() => {
    void load();
    return () => {
      generation.current++;
    };
  }, [load]);
  const upload = async (files: FileList | null) => {
    if (!files?.length) return;
    setUploading(true);
    setError("");
    try {
      for (const file of Array.from(files)) {
        const uploaded = await uploadFileWithPresignedUrl(file);
        await workPost(
          updatingResource
            ? `/api/v1/users/work/resources/${encodeURIComponent(updatingResource)}/versions`
            : "/api/v1/users/work/resources",
          {
            url: uploaded.accessUrl,
            filename: uploaded.filename,
            project_id: projectId,
          },
        );
      }
      await load();
    } catch (e) {
      setError(workError(e));
    } finally {
      setUploading(false);
      setUpdatingResource(null);
      if (inputRef.current) inputRef.current.value = "";
    }
  };
  return (
    <section className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold">
            {t("workFiles.projectTitle")}
          </h2>
          <p className="mt-1 text-sm text-muted-foreground">
            {t("workFiles.projectHint")}
          </p>
        </div>
        <div className="flex gap-2">
          <Button
            aria-label={t("workFiles.refresh")}
            variant="ghost"
            size="sm"
            onClick={() => void load()}
            disabled={loading}
          >
            <RefreshCw className="size-4" />
          </Button>
          {canWrite && (
            <Button
              size="sm"
              disabled={uploading}
              onClick={() => {
                setUpdatingResource(null);
                inputRef.current?.click();
              }}
            >
              {uploading ? (
                <Loader2 className="size-4 animate-spin" />
              ) : (
                <Upload className="size-4" />
              )}
              {t("workFiles.uploadProject")}
            </Button>
          )}
        </div>
      </div>
      <input
        ref={inputRef}
        type="file"
        multiple={!updatingResource}
        className="hidden"
        onChange={(e) => void upload(e.target.files)}
      />
      {error && <WorkError message={error} retry={() => void load()} />}
      {loading ? (
        <WorkLoading />
      ) : !items.length && !error ? (
        <WorkEmpty
          title={t("workFiles.projectEmpty")}
          description={t("workFiles.projectEmptyHint")}
        />
      ) : (
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {items.map((item) => {
            const selected =
              selectedVersions[item.id] || item.current_version_id;
            const version = item.versions?.find((v) => v.id === selected);
            return (
              <article
                key={item.id}
                className="min-w-0 space-y-3 rounded-xl border bg-card p-4"
              >
                <div className="flex gap-3">
                  <div className="rounded-lg bg-accent p-2 text-primary">
                    <FileText className="size-5" />
                  </div>
                  <div className="min-w-0">
                    <h3
                      className="truncate text-sm font-medium"
                      title={item.filename}
                    >
                      {item.filename}
                    </h3>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {workSize(version?.size)} {workTime(version?.created_at)}
                    </p>
                  </div>
                </div>
                <div className="flex items-center justify-between gap-2">
                  <FileVersionMenu
                      filename={item.filename}
                      versions={resourceVersions(item)}
                      currentID={item.current_version_id}
                      selectedID={selected}
                      onChange={(id) =>
                        setSelectedVersions((prev) => ({
                          ...prev,
                          [item.id]: id,
                        }))
                      }
                  />
                  {canWrite && (
                    <Button
                      size="sm"
                      variant="outline"
                      className="ml-auto"
                      disabled={uploading}
                      onClick={() => {
                        setUpdatingResource(item.id);
                        inputRef.current?.click();
                      }}
                    >
                      {t("workFiles.uploadVersion")}
                    </Button>
                  )}
                </div>
              </article>
            );
          })}
        </div>
      )}
    </section>
  );
}
