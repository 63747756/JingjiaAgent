import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  BookOpen,
  Plus,
  Pencil,
  Trash2,
  Upload,
  Loader2,
  ExternalLink,
} from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import {
  workRequest,
  workPost,
  workItems,
  workError,
  type WorkSkill,
} from "@/lib/work-api";
import { WorkEmpty, WorkError, WorkLoading } from "./work-common";
import { SkillHubUploadDialog } from "./skillhub-upload-dialog";

const base = "/api/v1/users/work";
export function WorkSkills() {
  const { t } = useTranslation();
  const [items, setItems] = useState<WorkSkill[]>([]),
    [loading, setLoading] = useState(true),
    [error, setError] = useState(""),
    [query, setQuery] = useState(""),
    [filter, setFilter] = useState("all"),
    [editor, setEditor] = useState<WorkSkill | "new" | null>(null),
    [upload, setUpload] = useState<WorkSkill | null>(null),
    [busy, setBusy] = useState("");
  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const data = await workRequest<{ items: WorkSkill[] }>(base + "/skills");
      setItems(workItems(data).filter((s) => s.scope.type === "user"));
    } catch (e) {
      setError(workError(e));
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);
  const update = async (s: WorkSkill, enabled: boolean) => {
    setBusy(s.id);
    try {
      await workRequest(base + `/skills/${s.id}/status`, {
        method: "PUT",
        body: JSON.stringify({ enabled }),
      });
      setItems((prev) =>
        prev.map((v) => (v.id === s.id ? { ...v, enabled } : v)),
      );
      toast.success(
        t(
          enabled
            ? "skillsExperience.enabledToast"
            : "skillsExperience.disabledToast",
        ),
      );
    } catch (e) {
      toast.error(workError(e));
    } finally {
      setBusy("");
    }
  };
  const remove = async (s: WorkSkill) => {
    if (!window.confirm(t("skillsExperience.deleteConfirm", { name: s.name })))
      return;
    setBusy(s.id);
    try {
      await workRequest(base + `/skills/${s.id}`, { method: "DELETE" });
      setItems((prev) => prev.filter((v) => v.id !== s.id));
    } catch (e) {
      toast.error(workError(e));
    } finally {
      setBusy("");
    }
  };
  const edit = async (s: WorkSkill) => {
    setBusy(s.id);
    try {
      setEditor(await workRequest<WorkSkill>(base + `/skills/${s.id}`));
    } catch (e) {
      toast.error(workError(e));
    } finally {
      setBusy("");
    }
  };
  const visible = items.filter(
    (s) =>
      (filter === "all" ||
        (filter === "market"
          ? s.source_type === "market"
          : s.source_type !== "market")) &&
      [s.name, s.description, ...(s.tags || [])]
        .join(" ")
        .toLocaleLowerCase()
        .includes(query.toLocaleLowerCase()),
  );
  return (
    <section className="w-full min-w-0 space-y-5">
      <div className="flex flex-wrap justify-between gap-3">
        <div className="flex gap-2">
          {["all", "market", "personal"].map((f) => (
            <Button
              key={f}
              size="sm"
              variant={filter === f ? "secondary" : "ghost"}
              onClick={() => setFilter(f)}
            >
              {t(`skillsExperience.filter.${f}`)}
              {f === "all" && (
                <span className="text-muted-foreground">{items.length}</span>
              )}
            </Button>
          ))}
        </div>
        <div className="flex w-full gap-2 sm:w-auto">
          <Input
            aria-label={t("skillsExperience.search")}
            placeholder={t("skillsExperience.search")}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="min-w-0 flex-1 sm:w-80"
          />
          <Button className="shrink-0" onClick={() => setEditor("new")}>
            <Plus className="size-4" />
            {t("skillsExperience.addSkill")}
          </Button>
        </div>
      </div>
      {error && <WorkError message={error} retry={() => void load()} />}
      {loading ? (
        <WorkLoading />
      ) : !visible.length ? (
        <WorkEmpty
          title={t("skillsExperience.empty")}
          description={t("skillsExperience.emptyHint")}
        />
      ) : (
        <div className="space-y-3">
          {visible.map((s) => (
            <article
              key={s.id}
              className="flex min-w-0 flex-wrap items-center gap-3 rounded-xl bg-muted/40 p-4"
            >
              <div className="rounded-xl bg-background p-3">
                <BookOpen className="size-5" />
              </div>
              <div className="min-w-0 flex-1">
                <h2 className="truncate text-sm font-medium">{s.name}</h2>
                <p className="mt-1 line-clamp-2 text-xs text-muted-foreground">
                  {s.description}
                </p>
                <div className="mt-1 flex gap-2 text-xs text-muted-foreground">
                  <span>
                    {t(
                      s.source_type === "market"
                        ? "skillsExperience.origin.market"
                        : "skillsExperience.origin.personal",
                    )}
                  </span>
                  <span>
                    {s.source_version ||
                      s.versions.find((v) => v.id === s.active_version_id)
                        ?.version}
                  </span>
                  {s.source_url && (
                    <a
                      href={s.source_url}
                      target="_blank"
                      rel="noopener noreferrer"
                      aria-label={t("skillsExperience.source")}
                    >
                      <ExternalLink className="size-3.5" />
                    </a>
                  )}
                </div>
              </div>
              <div className="ml-auto flex items-center gap-1">
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label={t("skillsExperience.edit", { name: s.name })}
                  disabled={!!busy}
                  onClick={() => void edit(s)}
                >
                  <Pencil className="size-4" />
                </Button>
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label={t("skillsExperience.upload", { name: s.name })}
                  disabled={!!busy}
                  onClick={() => setUpload(s)}
                >
                  <Upload className="size-4" />
                </Button>
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label={t("skillsExperience.delete", { name: s.name })}
                  disabled={!!busy}
                  onClick={() => void remove(s)}
                >
                  <Trash2 className="size-4" />
                </Button>
                <Switch
                  aria-label={t("skillsExperience.enable", { name: s.name })}
                  checked={s.enabled}
                  disabled={!!busy}
                  onCheckedChange={(v) => void update(s, v)}
                />
              </div>
            </article>
          ))}
        </div>
      )}
      {editor && (
        <PersonalSkillEditor
          skill={editor}
          onClose={() => setEditor(null)}
          onSaved={() => {
            setEditor(null);
            void load();
          }}
        />
      )}
      {upload && (
        <SkillHubUploadDialog skill={upload} onClose={() => setUpload(null)} />
      )}
    </section>
  );
}

function PersonalSkillEditor({
  skill,
  onClose,
  onSaved,
}: {
  skill: WorkSkill | "new";
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const [mode, setMode] = useState("text"),
    [name, setName] = useState(skill === "new" ? "" : skill.name),
    [description, setDescription] = useState(
      skill === "new" ? "" : skill.description,
    ),
    [content, setContent] = useState(
      skill === "new" ? "" : skill.content || "",
    ),
    [file, setFile] = useState<File | null>(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const save = async () => {
    setBusy(true);
    setError("");
    try {
      if (mode === "zip") {
        if (!file) return;
        const data = new FormData();
        data.append("file", file);
        await workRequest(
          base +
            (skill === "new"
              ? "/skills/package"
              : `/skills/${skill.id}/package`),
          { method: "POST", body: data },
        );
      } else {
        const raw = content.trim().startsWith("---")
          ? content
          : `---\nname: ${JSON.stringify(name)}\ndescription: ${JSON.stringify(description)}\n---\n\n${content}`;
        await workPost(
          base + (skill === "new" ? "/skills" : `/skills/${skill.id}/versions`),
          { name, description, content: raw },
        );
      }
      toast.success(t("skillsExperience.saved"));
      onSaved();
    } catch (e) {
      setError(workError(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onOpenChange={(v) => {
        if (!v && !busy) onClose();
      }}
    >
      <DialogContent className="flex max-h-[90dvh] flex-col sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>
            {t(
              skill === "new"
                ? "skillsExperience.addSkill"
                : "skillsExperience.editSkill",
            )}
          </DialogTitle>
          <DialogDescription>
            {t("skillsExperience.editorHint")}
          </DialogDescription>
        </DialogHeader>
        <Tabs
          value={mode}
          onValueChange={setMode}
          className="min-h-0 overflow-y-auto"
        >
          <TabsList>
            <TabsTrigger value="text">
              {t("skillsExperience.write")}
            </TabsTrigger>
            <TabsTrigger value="zip">{t("skillsExperience.zip")}</TabsTrigger>
          </TabsList>
          <TabsContent value="text" className="space-y-4">
            <label className="block text-sm">
              {t("skillsExperience.name")}
              <Input
                value={name}
                disabled={busy || skill !== "new"}
                maxLength={120}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
            <label className="block text-sm">
              {t("skillsExperience.description")}
              <Input
                value={description}
                disabled={busy}
                maxLength={4096}
                onChange={(e) => setDescription(e.target.value)}
              />
            </label>
            <label className="block text-sm">
              SKILL.md
              <Textarea
                value={content}
                disabled={busy}
                onChange={(e) => setContent(e.target.value)}
                maxLength={262144}
                className="min-h-64 font-mono text-xs"
                placeholder={t("skillsExperience.contentPlaceholder")}
              />
            </label>
          </TabsContent>
          <TabsContent value="zip" className="space-y-4">
            <p className="text-sm text-muted-foreground">
              {t("skillsExperience.zipHint")}
            </p>
            <Input
              type="file"
              accept=".zip,application/zip"
              aria-label={t("skillsExperience.zip")}
              disabled={busy}
              onChange={(e) => setFile(e.target.files?.[0] || null)}
            />
            {file && <p className="text-xs">{file.name}</p>}
          </TabsContent>
        </Tabs>
        {error && <WorkError message={error} />}
        <div className="flex justify-end gap-2 border-t pt-4">
          <Button variant="outline" disabled={busy} onClick={onClose}>
            {t("skillsExperience.cancel")}
          </Button>
          <Button
            disabled={
              busy || (mode === "zip" ? !file : !name.trim() || !content.trim())
            }
            onClick={() => void save()}
          >
            {busy && <Loader2 className="size-4 animate-spin" />}
            {t("skillsExperience.save")}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
