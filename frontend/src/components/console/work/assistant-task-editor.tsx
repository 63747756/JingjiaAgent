import { useEffect, useRef, useState } from "react";
import {
  ArrowDown,
  ArrowUp,
  ChevronDown,
  Copy,
  Eye,
  EyeOff,
  MoreHorizontal,
  Pencil,
  Plus,
  Trash2,
  Sparkles,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { workRequestID, type WorkTaskPreset } from "@/lib/work-api";
import { cn } from "@/lib/utils";
import { taskPresetIcons as icons } from "./task-preset-icons";

export function AssistantTaskEditor({
  tasks,
  disabled,
  onChange,
  errors = {},
  focusRequest,
}: {
  tasks: WorkTaskPreset[];
  disabled: boolean;
  onChange: (tasks: WorkTaskPreset[]) => void;
  errors?: Record<string, { name?: string; description?: string }>;
  focusRequest?: { taskId?: string; version: number };
}) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState<string>();
  const [previewId, setPreviewId] = useState<string>();
  const [focusNameId, setFocusNameId] = useState<string>();
  const editor = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (focusRequest?.taskId) setExpanded(focusRequest.taskId);
  }, [focusRequest]);
  useEffect(() => {
    if (!focusNameId) return;
    const input = editor.current?.querySelector<HTMLInputElement>(
      `[data-task-id="${focusNameId}"] input`,
    );
    input?.focus();
    input?.scrollIntoView({ block: "nearest" });
    setFocusNameId(undefined);
  }, [focusNameId, tasks]);
  const update = (id: string, patch: Partial<WorkTaskPreset>) =>
    onChange(
      tasks.map((task) => (task.id === id ? { ...task, ...patch } : task)),
    );
  const move = (index: number, offset: number) => {
    const next = [...tasks];
    [next[index], next[index + offset]] = [next[index + offset], next[index]];
    onChange(next);
  };
  const add = (source?: WorkTaskPreset) => {
    const task: WorkTaskPreset = source
      ? {
          ...source,
          id: workRequestID(),
          name: t("assistantProfiles.copyName", { name: source.name }).slice(
            0,
            120,
          ),
        }
      : {
          id: workRequestID(),
          name: "",
          description: "",
          icon: "sparkles",
          enabled: true,
        };
    const next = [...tasks];
    next.splice(
      source
        ? tasks.findIndex((item) => item.id === source.id) + 1
        : tasks.length,
      0,
      task,
    );
    onChange(next);
    setExpanded(task.id);
    setFocusNameId(task.id);
  };
  const visible = tasks.filter((task) => task.enabled);
  const preview = visible.find((task) => task.id === previewId);
  return (
    <div ref={editor} className="space-y-6">
      <div>
        <div className="flex items-center justify-between gap-3">
          <h3 className="text-sm font-medium">
            {t("assistantProfiles.presets")}{" "}
            <span className="ml-1 font-normal text-muted-foreground">
              {tasks.length}/32
            </span>
          </h3>
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={disabled || tasks.length >= 32}
            onClick={() => add()}
          >
            <Plus className="size-3.5" />
            {t("assistantProfiles.addPreset")}
          </Button>
        </div>
        <p className="mt-2 text-xs leading-5 text-muted-foreground">
          {t("assistantProfiles.presetsHint")}
        </p>
      </div>
      <div className="space-y-2">
        {!tasks.length && (
          <div className="rounded-xl border border-dashed p-6 text-center text-sm text-muted-foreground">
            {t("assistantProfiles.emptyPresets")}
          </div>
        )}
        {tasks.map((task, index) => {
          const open = expanded === task.id;
          const invalid = Boolean(
            errors[task.id]?.name || errors[task.id]?.description,
          );
          const Icon = icons[task.icon] || Sparkles;
          return (
            <div
              key={task.id}
              data-task-id={task.id}
              className={cn(
                "rounded-xl border",
                open && "border-primary/30",
                invalid && "border-destructive/50",
              )}
            >
              <div className="flex items-center gap-2 px-3 py-3 sm:gap-3">
                <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-muted text-xs text-muted-foreground">
                  {index + 1}
                </span>
                <button
                  type="button"
                  className="min-w-0 flex-1 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  disabled={disabled}
                  aria-expanded={open}
                  aria-controls={`task-editor-${task.id}`}
                  onClick={() => setExpanded(open ? undefined : task.id)}
                >
                  <span className="block truncate text-sm font-medium">
                    {task.name.trim() || t("assistantProfiles.untitledPreset")}
                  </span>
                  <span className="mt-1 block truncate text-xs text-muted-foreground">
                    {t(
                      task.enabled
                        ? "assistantProfiles.visible"
                        : "assistantProfiles.hidden",
                    )}
                  </span>
                </button>
                {invalid && (
                  <Badge
                    variant="outline"
                    className="hidden text-destructive sm:inline-flex"
                  >
                    {t("assistantProfiles.incomplete")}
                  </Badge>
                )}
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  disabled={disabled}
                  aria-label={t("assistantProfiles.editPreset", {
                    name: task.name || index + 1,
                  })}
                  aria-expanded={open}
                  aria-controls={`task-editor-${task.id}`}
                  onClick={() => setExpanded(open ? undefined : task.id)}
                  className="gap-1.5 px-2"
                >
                  <Pencil className="size-3.5" />
                  <span className="hidden sm:inline">
                    {t("assistantProfiles.edit")}
                  </span>
                  <ChevronDown
                    className={cn(
                      "size-3 transition-transform",
                      open && "rotate-180",
                    )}
                  />
                </Button>
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button
                      type="button"
                      size="icon"
                      variant="ghost"
                      className="size-8 shrink-0"
                      disabled={disabled}
                      aria-label={t("assistantProfiles.presetActions", {
                        name: task.name || index + 1,
                      })}
                    >
                      <MoreHorizontal className="size-4" />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end" className="w-40">
                    <DropdownMenuItem
                      disabled={tasks.length >= 32}
                      onSelect={() => add(task)}
                    >
                      <Copy />
                      {t("assistantProfiles.duplicate")}
                    </DropdownMenuItem>
                    <DropdownMenuItem
                      disabled={index === 0}
                      onSelect={() => move(index, -1)}
                    >
                      <ArrowUp />
                      {t("assistantTasks.moveUp")}
                    </DropdownMenuItem>
                    <DropdownMenuItem
                      disabled={index === tasks.length - 1}
                      onSelect={() => move(index, 1)}
                    >
                      <ArrowDown />
                      {t("assistantTasks.moveDown")}
                    </DropdownMenuItem>
                    <DropdownMenuItem
                      onSelect={() =>
                        update(task.id, { enabled: !task.enabled })
                      }
                    >
                      {task.enabled ? <EyeOff /> : <Eye />}
                      {t(
                        task.enabled
                          ? "assistantProfiles.hide"
                          : "assistantProfiles.show",
                      )}
                    </DropdownMenuItem>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem
                      variant="destructive"
                      onSelect={() => {
                        onChange(tasks.filter((item) => item.id !== task.id));
                        if (open) setExpanded(undefined);
                      }}
                    >
                      <Trash2 />
                      {t("assistantProfiles.delete")}
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
              {open && (
                <div
                  id={`task-editor-${task.id}`}
                  className="space-y-4 border-t bg-muted/20 p-4"
                >
                  <div className="space-y-2">
                    <label
                      htmlFor={`task-name-${task.id}`}
                      className="text-xs font-medium"
                    >
                      {t("assistantTasks.taskName")}{" "}
                      <span className="text-destructive">*</span>
                    </label>
                    <Input
                      id={`task-name-${task.id}`}
                      value={task.name}
                      maxLength={120}
                      disabled={disabled}
                      placeholder={t("assistantProfiles.taskNamePlaceholder")}
                      aria-invalid={Boolean(errors[task.id]?.name)}
                      aria-describedby={
                        errors[task.id]?.name
                          ? `task-name-error-${task.id}`
                          : undefined
                      }
                      onChange={(e) =>
                        update(task.id, { name: e.target.value })
                      }
                    />
                    {errors[task.id]?.name && (
                      <p
                        id={`task-name-error-${task.id}`}
                        className="text-xs text-destructive"
                      >
                        {errors[task.id].name}
                      </p>
                    )}
                  </div>
                  <div className="space-y-2">
                    <label
                      htmlFor={`task-description-${task.id}`}
                      className="text-xs font-medium"
                    >
                      {t("assistantProfiles.taskDescription")}{" "}
                      <span className="text-destructive">*</span>
                    </label>
                    <p className="text-xs leading-5 text-muted-foreground">
                      {t("assistantProfiles.taskDescriptionHint")}
                    </p>
                    <Textarea
                      id={`task-description-${task.id}`}
                      className="min-h-28 bg-background"
                      value={task.description}
                      maxLength={8192}
                      disabled={disabled}
                      placeholder={t(
                        "assistantTasks.taskDescriptionPlaceholder",
                      )}
                      aria-invalid={Boolean(errors[task.id]?.description)}
                      aria-describedby={
                        errors[task.id]?.description
                          ? `task-description-error-${task.id}`
                          : undefined
                      }
                      onChange={(e) =>
                        update(task.id, { description: e.target.value })
                      }
                    />
                    {errors[task.id]?.description && (
                      <p
                        id={`task-description-error-${task.id}`}
                        className="text-xs text-destructive"
                      >
                        {errors[task.id].description}
                      </p>
                    )}
                  </div>
                  <div className="flex flex-wrap items-end justify-between gap-4">
                    <div className="space-y-2">
                      <label
                        htmlFor={`task-icon-${task.id}`}
                        className="text-xs font-medium"
                      >
                        {t("assistantProfiles.taskIcon")}
                      </label>
                      <Select
                        value={task.icon}
                        disabled={disabled}
                        onValueChange={(value) =>
                          update(task.id, {
                            icon: value as WorkTaskPreset["icon"],
                          })
                        }
                      >
                        <SelectTrigger
                          id={`task-icon-${task.id}`}
                          className="w-40 bg-background"
                        >
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {Object.entries(icons).map(([icon, OptionIcon]) => (
                            <SelectItem key={icon} value={icon}>
                              <span className="flex items-center gap-2">
                                <OptionIcon className="size-3.5" />
                                {t(`assistantTasks.icon.${icon}`)}
                              </span>
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                    <div className="flex items-center gap-3 pb-2">
                      <label
                        htmlFor={`task-visible-${task.id}`}
                        className="text-xs"
                      >
                        {t("assistantProfiles.showOnHome")}
                      </label>
                      <Switch
                        id={`task-visible-${task.id}`}
                        disabled={disabled}
                        checked={task.enabled}
                        onCheckedChange={(value) =>
                          update(task.id, { enabled: value })
                        }
                      />
                    </div>
                  </div>
                  <p className="text-xs leading-5 text-muted-foreground">
                    <Icon className="mr-1 inline size-3.5" />
                    {t("assistantProfiles.taskIconHint")}
                  </p>
                </div>
              )}
            </div>
          );
        })}
      </div>
      <section
        className="rounded-xl border bg-muted/20 p-4"
        aria-label={t("assistantProfiles.previewTitle")}
      >
        <div className="flex items-center gap-2 text-sm font-medium">
          <Eye className="size-4 text-muted-foreground" />
          {t("assistantProfiles.previewTitle")}
        </div>
        <p className="mt-1.5 text-xs leading-5 text-muted-foreground">
          {t("assistantProfiles.previewHint")}
        </p>
        <div className="mt-4 flex flex-wrap gap-2 p-1">
          {visible.map((task) => {
            const Icon = icons[task.icon] || Sparkles;
            return (
              <Button
                key={task.id}
                type="button"
                size="sm"
                variant="outline"
                aria-pressed={previewId === task.id}
                className={cn(
                  "rounded-full font-normal",
                  previewId === task.id &&
                    "border-primary/30 bg-primary/5 text-primary",
                )}
                onClick={() =>
                  setPreviewId(previewId === task.id ? undefined : task.id)
                }
              >
                <Icon className="size-3.5" />
                {task.name.trim() || t("assistantProfiles.untitledPreset")}
              </Button>
            );
          })}
        </div>
        {!visible.length && (
          <p className="mt-3 text-xs text-muted-foreground">
            {t("assistantProfiles.noVisiblePresets")}
          </p>
        )}
        {preview && (
          <div className="mt-3 space-y-2 rounded-lg border bg-background p-3">
            <p className="text-xs font-medium">
              {t("assistantProfiles.previewDescription")}
            </p>
            <p
              className="max-h-32 overflow-y-auto whitespace-pre-wrap break-words text-sm leading-6"
              data-slot="preset-preview-description"
            >
              {preview.description || t("assistantProfiles.noDescription")}
            </p>
          </div>
        )}
      </section>
    </div>
  );
}
