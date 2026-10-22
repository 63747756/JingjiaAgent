import { WorkSkillPicker } from "./work-skill-picker";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertCircle, ChevronDown, Loader2 } from "lucide-react";
import type { DomainModel, DomainHost, DomainImage } from "@/api/Api";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { WorkError } from "./work-common";
import { AssistantTaskEditor } from "./assistant-task-editor";
import { profileIssues, type ProfileTab } from "@/lib/assistant-profile-form";
import {
  workRequest,
  workError,
  type WorkProfile,
  type WorkSkill,
} from "@/lib/work-api";

export type ExecutionResources = {
  models: DomainModel[];
  hosts: DomainHost[];
  images: DomainImage[];
};

export function ProfileEditor({
  initial,
  resources,
  skills,
  onClose,
  onSaved,
}: {
  initial: WorkProfile;
  resources: ExecutionResources;
  skills: WorkSkill[];
  onClose: () => void;
  onSaved: (saved: WorkProfile) => void;
}) {
  const { t } = useTranslation();
  const [form, setForm] = useState(initial);
  const [tab, setTab] = useState<ProfileTab>("basic");
  const [advanced, setAdvanced] = useState(false);
  const [attempted, setAttempted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [focusRequest, setFocusRequest] = useState<{
    taskId?: string;
    version: number;
  }>({ version: 0 });
  const body = useRef<HTMLDivElement>(null);
  const update = <K extends keyof WorkProfile>(key: K, value: WorkProfile[K]) =>
    setForm((previous) => ({ ...previous, [key]: value }));
  const issues = profileIssues(form);
  const fieldError = (field: keyof WorkProfile) =>
    attempted
      ? issues.find((issue) => issue.field === field && !issue.taskId)
      : undefined;
  const errorText = (field: keyof WorkProfile) => {
    const issue = fieldError(field);
    return issue ? (
      <p
        id={`profile-${field}-error`}
        className="mt-1.5 text-xs text-destructive"
      >
        {t(`assistantProfiles.validation.${issue.key}`)}
      </p>
    ) : null;
  };
  const invalidProps = (field: keyof WorkProfile) => ({
    "aria-invalid": Boolean(fieldError(field)),
    "aria-describedby": fieldError(field)
      ? `profile-${field}-error`
      : undefined,
    "data-profile-field": field,
  });
  const taskErrors: Record<string, { name?: string; description?: string }> =
    {};
  if (attempted)
    for (const issue of issues) {
      if (issue.taskId && issue.part) {
        taskErrors[issue.taskId] ||= {};
        taskErrors[issue.taskId][issue.part] = t(
          `assistantProfiles.validation.${issue.key}`,
        );
      }
    }
  const revealIssue = () => {
    const issue = issues[0];
    if (!issue) return;
    setTab(issue.tab);
    setFocusRequest((previous) => ({
      taskId: issue.taskId,
      version: previous.version + 1,
    }));
    if (
      ["core", "memory"].includes(issue.field)
    )
      setAdvanced(true);
    requestAnimationFrame(() =>
      requestAnimationFrame(() => {
        const target = issue.taskId
          ? body.current?.querySelector<HTMLElement>(
              `[data-task-id="${issue.taskId}"] [aria-invalid="true"]`,
            )
          : body.current?.querySelector<HTMLElement>(
              `[data-profile-field="${issue.field}"]`,
            );
        target?.focus();
        target?.scrollIntoView({ block: "nearest" });
      }),
    );
  };
  const save = async () => {
    setAttempted(true);
    if (issues.length) {
      revealIssue();
      return;
    }
    setBusy(true);
    setError("");
    try {
      const saved = await workRequest<WorkProfile>(
        initial.id
          ? `/api/v1/users/work/profiles/${encodeURIComponent(initial.id)}`
          : "/api/v1/users/work/profiles",
        { method: initial.id ? "PUT" : "POST", body: JSON.stringify(form) },
      );
      onSaved(saved);
    } catch (e) {
      setError(workError(e));
    } finally {
      setBusy(false);
    }
  };
  const tabs = ["basic", "tasks", "execution"] as const;
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <DialogContent className="flex h-[min(760px,90dvh)] min-h-0 flex-col gap-0 overflow-hidden p-0 sm:max-w-5xl">
        <DialogHeader className="shrink-0 border-b px-5 py-5 sm:px-6">
          <DialogTitle>
            {t(
              initial.id
                ? "assistantProfiles.editAssistant"
                : "assistantProfiles.newAssistant",
            )}
          </DialogTitle>
          <DialogDescription>
            {t("assistantProfiles.editorHint")}
          </DialogDescription>
        </DialogHeader>
        <Tabs
          value={tab}
          onValueChange={(value) => setTab(value as ProfileTab)}
          className="min-h-0 flex-1 gap-0"
        >
          <div className="shrink-0 border-b px-5 py-3 sm:px-6">
            <TabsList className="w-full sm:w-auto">
              {tabs.map((value) => (
                <TabsTrigger key={value} value={value}>
                  {t(`assistantProfiles.tabs.${value}`)}
                  {attempted && issues.some((issue) => issue.tab === value) && (
                    <AlertCircle className="size-3.5 text-destructive" />
                  )}
                </TabsTrigger>
              ))}
            </TabsList>
          </div>
          <div
            ref={body}
            className="min-h-0 flex-1 overflow-y-auto px-5 py-5 sm:px-6"
            data-slot="assistant-editor-body"
          >
            <TabsContent value="basic" className="m-0 space-y-5">
              <label
                className="block space-y-2 text-sm font-medium"
                htmlFor="profile-name"
              >
                {t("assistantProfiles.name")}{" "}
                <span className="text-destructive">*</span>
                <Input
                  id="profile-name"
                  value={form.name}
                  maxLength={120}
                  disabled={busy}
                  onChange={(e) => update("name", e.target.value)}
                  {...invalidProps("name")}
                />
                {errorText("name")}
              </label>
              <div className="space-y-2">
                <label
                  className="text-sm font-medium"
                  htmlFor="profile-description"
                >
                  {t("assistantProfiles.description")}
                </label>
                <p className="text-xs leading-5 text-muted-foreground">
                  {t("assistantProfiles.descriptionHint")}
                </p>
                <Textarea
                  id="profile-description"
                  value={form.description}
                  maxLength={4096}
                  disabled={busy}
                  onChange={(e) => update("description", e.target.value)}
                  {...invalidProps("description")}
                />
                {errorText("description")}
              </div>
              <div className="space-y-2">
                <label
                  className="text-sm font-medium"
                  htmlFor="profile-instructions"
                >
                  {t("assistantProfiles.instructions")}
                </label>
                <p className="text-xs leading-5 text-muted-foreground">
                  {t("assistantProfiles.instructionsHint")}
                </p>
                <Textarea
                  id="profile-instructions"
                  className="min-h-36"
                  value={form.instructions}
                  maxLength={8192}
                  placeholder={t("assistantProfiles.instructionsPlaceholder")}
                  disabled={busy}
                  onChange={(e) => update("instructions", e.target.value)}
                  {...invalidProps("instructions")}
                />
                {errorText("instructions")}
              </div>
            </TabsContent>
            <TabsContent value="tasks" className="m-0">
              <AssistantTaskEditor
                tasks={form.task_presets}
                disabled={busy}
                errors={taskErrors}
                focusRequest={focusRequest}
                onChange={(tasks) => update("task_presets", tasks)}
              />
              <div data-profile-field="task_presets" tabIndex={-1}>
                {errorText("task_presets")}
              </div>
            </TabsContent>
            <TabsContent value="execution" className="m-0 space-y-6">
              <div>
                <h3 className="text-sm font-medium">
                  {t("assistantProfiles.resources")}
                </h3>
                <p className="mt-1 text-xs leading-5 text-muted-foreground">
                  {t("assistantProfiles.resourcesHint")}
                </p>
              </div>
              <div className="grid gap-5 sm:grid-cols-2">
                {(
                  [
                    [
                      "model_id",
                      resources.models.map((model) => ({
                        id: model.id || "",
                        name: model.remark || model.model || model.id,
                      })),
                    ],
                    [
                      "host_id",
                      [{ id: "__auto__", name: t("assistantProfiles.autoHost") }, ...resources.hosts.map((host) => ({
                        id: host.id || "",
                        name: host.name || host.id,
                      }))],
                    ],
                    [
                      "image_id",
                      resources.images.map((image) => ({
                        id: image.id || "",
                        name: image.remark || image.name || image.id,
                      })),
                    ],
                  ] as const
                ).map(([field, options]) => (
                  <div key={field} className="space-y-2">
                    <label
                      htmlFor={`profile-${field}`}
                      className="text-sm font-medium"
                    >
                      {t(`assistantProfiles.fields.${field}`)}{" "}
                      {field !== "host_id" && <span className="text-destructive">*</span>}
                    </label>
                    <Select
                      value={field === "host_id" && !form.host_id ? "__auto__" : form[field]}
                      disabled={busy}
                      onValueChange={(value) => update(field, field === "host_id" && value === "__auto__" ? "" : value)}
                    >
                      <SelectTrigger
                        id={`profile-${field}`}
                        className="w-full"
                        {...invalidProps(field)}
                      >
                        <SelectValue
                          placeholder={t("assistantProfiles.selectResource")}
                        />
                      </SelectTrigger>
                      <SelectContent>
                        {options
                          .filter((option) => option.id)
                          .map((option) => (
                            <SelectItem key={option.id} value={option.id}>
                              {option.name}
                            </SelectItem>
                          ))}
                      </SelectContent>
                    </Select>
                    {!options.length && (
                      <p className="text-xs text-muted-foreground">
                        {t("assistantProfiles.noAuthorizedResource")}
                      </p>
                    )}
                    {errorText(field)}
                  </div>
                ))}
                <div className="space-y-2">
                  <label
                    htmlFor="profile-cli_name"
                    className="text-sm font-medium"
                  >
                    {t("assistantProfiles.runner")}
                  </label>
                  <Select
                    value={form.cli_name}
                    disabled={busy}
                    onValueChange={(value) =>
                      update("cli_name", value as WorkProfile["cli_name"])
                    }
                  >
                    <SelectTrigger id="profile-cli_name" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="opencode">OpenCode</SelectItem>
                      <SelectItem value="claude">Claude Code</SelectItem>
                      <SelectItem value="codex">Codex</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              </div>
              <fieldset
                className="space-y-3"
                data-profile-field="required_skill_ids"
              >
                <legend className="text-sm font-medium">
                  {t("assistantProfiles.requiredSkills")}
                </legend>
                <p className="text-xs leading-5 text-muted-foreground">
                  {t("assistantProfiles.skillsHint")}
                </p>
                <WorkSkillPicker
                  skills={skills.filter((skill) => skill.scope.type !== "user")}
                  selected={form.required_skill_ids}
                  disabled={busy}
                  onChange={(id, checked) => setForm((previous) => ({
                    ...previous,
                    required_skill_ids: checked ? [...previous.required_skill_ids, id] : previous.required_skill_ids.filter((v) => v !== id),
                    mentionable_skill_ids: checked ? (previous.mentionable_skill_ids || []) : (previous.mentionable_skill_ids || []).filter((v) => v !== id),
                  }))}
                />
                {form.required_skill_ids.length > 0 && <div className="space-y-2">
                  <p className="text-xs leading-5 text-muted-foreground">{t("assistantProfiles.mentionableHint")}</p>
                  <div className="divide-y rounded-lg border">
                    {form.required_skill_ids.map((id) => {
                      const skill = skills.find((item) => item.id === id);
                      return <div key={id} className="flex flex-wrap items-center justify-between gap-2 px-3 py-2.5" data-bound-skill={id}>
                        <span className="min-w-0 flex-1 truncate text-sm" title={skill?.name || id}>{skill?.name || id}</span>
                        <label className="flex shrink-0 cursor-pointer items-center gap-2 text-xs text-muted-foreground">
                          {t("assistantProfiles.allowMention")}
                          <Switch disabled={busy || !skill} checked={(form.mentionable_skill_ids || []).includes(id)}
                            aria-label={t("assistantProfiles.allowMentionFor", { name: skill?.name || id })}
                            onCheckedChange={(checked) => update("mentionable_skill_ids", checked ? [...(form.mentionable_skill_ids || []), id] : (form.mentionable_skill_ids || []).filter((value) => value !== id))} />
                        </label>
                      </div>;
                    })}
                  </div>
                </div>}
                {!skills.length && (
                  <p className="rounded-lg bg-muted/50 p-3 text-xs text-muted-foreground">
                    {t("assistantProfiles.noSkills")}
                  </p>
                )}
                {errorText("required_skill_ids")}
              </fieldset>
              <div className="rounded-xl border">
                <Button
                  type="button"
                  variant="ghost"
                  className="h-auto w-full justify-between px-4 py-3"
                  aria-expanded={advanced}
                  onClick={() => setAdvanced(!advanced)}
                >
                  <span className="text-sm">
                    {t("assistantProfiles.advanced")}
                  </span>
                  <ChevronDown
                    className={`size-4 transition-transform ${advanced ? "rotate-180" : ""}`}
                  />
                </Button>
                {advanced && (
                  <div className="grid grid-cols-2 gap-4 border-t p-4">
                    {(
                      [
                        ["core", form.core, 1, 32, 1],
                        [
                          "memory",
                          form.memory / 1073741824,
                          1,
                          128,
                          1073741824,
                        ],
                      ] as const
                    ).map(([field, value, min, max, multiplier]) => (
                      <div key={field}>
                        <label htmlFor={`profile-${field}`} className="text-xs">
                          {t(`assistantProfiles.fields.${field}`)}
                        </label>
                        <Input
                          id={`profile-${field}`}
                          className="mt-2"
                          type="number"
                          min={min}
                          max={max}
                          step={1}
                          value={value}
                          disabled={busy}
                          onChange={(e) =>
                            update(field, Number(e.target.value) * multiplier)
                          }
                          {...invalidProps(field)}
                        />
                        {errorText(field)}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </TabsContent>
          </div>
        </Tabs>
        <div className="shrink-0 space-y-3 border-t bg-background px-5 py-4 sm:px-6">
          {error && <WorkError message={error} />}
          <div className="flex flex-wrap items-center justify-between gap-3">
            {attempted && issues.length > 0 && (
              <div className="text-xs text-muted-foreground">
                <button
                  type="button"
                  className="flex items-center gap-1.5 text-destructive"
                  onClick={revealIssue}
                >
                  <AlertCircle className="size-3.5" />
                  {t("assistantProfiles.fixIssues", { count: issues.length })}
                </button>
              </div>
            )}
            <div className="ml-auto flex gap-2">
              <Button variant="outline" disabled={busy} onClick={onClose}>
                {t("assistantProfiles.cancel")}
              </Button>
              <Button disabled={busy} onClick={() => void save()}>
                {busy && <Loader2 className="size-4 animate-spin" />}
                {t("assistantProfiles.save")}
              </Button>
            </div>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
