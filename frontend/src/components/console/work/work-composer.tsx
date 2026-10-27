import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import {
  ArrowUp,
  FileText,
  Sparkles,
  X,
  Loader2,
  Bot,
  FolderOpen,
  GitBranch,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { MessageComposerFrame, MessageComposerTextarea } from "../message-composer";
import { useCommonData } from "@/components/console/data-provider";
import { SkillMentionChip, skillToMention, type SkillMention } from "./skill-mention-picker";
import {
  workRequest,
  workPost,
  workError,
  workRequestID,
  type WorkConfig,
  type WorkResource,
  type WorkTask,
  type WorkTaskInput,
  type WorkSkill,
  type WorkProject,
} from "@/lib/work-api";
import { uploadTaskFile, TaskUploadFileTooLargeError, MAX_TASK_UPLOAD_FILES, MAX_TASK_UPLOAD_FILE_SIZE_LABEL, MAX_TASK_UPLOAD_TOTAL_BYTES } from "../task/task-file-upload";
import { useBranches } from "@/hooks/useBranches";
import { WorkError } from "./work-common";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";
import {
  availableAssistants,
  chooseAssistantID,
  assistantTasks,
  applyTaskDescription,
} from "@/lib/assistant-tasks";
import { WorkSelect } from "./work-select";
import { WorkContextMenu } from "./work-context-menu";
import { useWorkFiles } from "./use-work-files";
import AddProjectDialog from "@/components/console/project/add-project";
import { taskPresetIcons } from "./task-preset-icons";
import ModelSelect from "../task/model-select";
import type { WorkModelOptions } from "@/lib/work-api";
import { initialTaskContent, newTaskDraftKey, parseTaskDraft, type IssueTaskSource } from "@/lib/issue-task-entry";

type TaskDraft = {
  content?: string;
  skillMention?: SkillMention;
  projectId?: string;
  branchSelection?: { project: string; name: string };
};

function readTaskDraft(key: string, projectId: string): TaskDraft | null {
  try {
    return parseTaskDraft<TaskDraft>(localStorage.getItem(key), projectId);
  } catch {
    return null;
  }
}

export function WorkComposer({
  projectId,
  compact = false,
  initialContent = "",
  issueSource,
}: {
  projectId?: string;
  compact?: boolean;
  initialContent?: string;
  issueSource?: IssueTaskSource & { title: string };
}) {
  const { t } = useTranslation();
  const {
    user,
    subscription,
    projects,
    reloadProjects,
    reloadUnlinkedTasks,
    reloadHistoricalTasks,
  } = useCommonData();
  const navigate = useNavigate();
  const location = useLocation();
  const initialProject = projectId ?? new URLSearchParams(location.search).get("project") ?? "";
  const [selectedProject, setSelectedProject] = useState(initialProject);
  const draftKey = newTaskDraftKey(user.id || "", selectedProject, issueSource);
  const initialDraft = user.id ? readTaskDraft(draftKey, selectedProject) : null;
  const [content, setContent] = useState(initialTaskContent(initialDraft, initialContent));
  const [projectDialogOpen, setProjectDialogOpen] = useState(false);
  const [branchSelection, setBranchSelection] = useState(initialDraft?.branchSelection || { project: "", name: "" });
  const [config, setConfig] = useState<WorkConfig | null>(null);
  const [skills, setSkills] = useState<WorkSkill[]>([]);
  const [skillMention, setSkillMention] = useState<SkillMention | null>(initialDraft?.skillMention || null);
  const [skillIssues, setSkillIssues] = useState<string[]>([]);
  const [skillsLoading, setSkillsLoading] = useState(false);
  const [loadedDraftKey, setLoadedDraftKey] = useState<string | null>(user.id ? draftKey : null);
  const draftSubmitted = useRef(false);
  const previousProject = useRef<string | null>(null);
  const { resources, loading: contextLoading, error: contextError, reload: reloadFiles } = useWorkFiles(selectedProject);
  useEffect(() => {
    if (!user.id || draftSubmitted.current) return;
    if (loadedDraftKey !== draftKey) {
      const saved = readTaskDraft(draftKey, selectedProject);
      setContent(initialTaskContent(saved, initialContent));
      setSkillMention(saved?.skillMention || null);
      setBranchSelection(saved?.branchSelection || { project: "", name: "" });
      // Commit the key with the restored input. Replayed effects must not save
      // the previous project's input over the destination project's draft.
      setLoadedDraftKey(draftKey);
      return;
    }
    try {
      if (content || skillMention || issueSource || branchSelection.name) localStorage.setItem(draftKey, JSON.stringify({ content, skillMention, projectId: selectedProject, branchSelection }));
      else localStorage.removeItem(draftKey);
    } catch { /* Draft storage must not prevent sending. */ }
  }, [content, skillMention, draftKey, loadedDraftKey, initialContent, user.id, issueSource, selectedProject, branchSelection]);
  const [profileID, setProfileID] = useState("");
  const [modelID, setModelID] = useState("");
  const [modelOptions, setModelOptions] = useState<WorkModelOptions | null>(null);
  const [modelError, setModelError] = useState("");
  const [modelsLoading, setModelsLoading] = useState(false);
  const [taskPresetID, setTaskPresetID] = useState("");
  const [versionIDs, setVersionIDs] = useState<string[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [error, setError] = useState("");
  const [configError, setConfigError] = useState("");
  const [attachments, setAttachments] = useState<
    { url: string; filename: string; size: number }[]
  >([]);
  const uploadBusy = useRef(false);
  const uploadRef = useRef<HTMLInputElement>(null);
  const contentRef = useRef<HTMLTextAreaElement>(null);
  const isComposing = useRef(false);
  const requestID = useRef(workRequestID());
  const requestSignature = useRef("");
  const project = projects.find((p) => p.id === selectedProject) as
    | WorkProject
    | undefined;
  const branch = branchSelection.project === selectedProject ? branchSelection.name : "";
  const { branches, loading: branchesLoading, loadingMore: branchesLoadingMore, hasMore: hasMoreBranches, loadMore: loadMoreBranches } = useBranches(
    project?.user?.id === user.id ? project?.git_identity_id : undefined,
    project?.full_name,
    { enabled: !!project?.repo_url },
  );
  const availableProfiles = useMemo(
    () => availableAssistants(config, project),
    [config, project],
  );
  const selectedAssistant = availableProfiles.find((p) => p.id === profileID);
  useEffect(() => {
    const controller = new AbortController();
    setModelID(""); setModelOptions(null); setModelError("");
    if (!profileID) return;
    setModelsLoading(true);
    void workRequest<WorkModelOptions>(`/api/v1/users/work/model-options?profile_id=${encodeURIComponent(profileID)}`, { signal: controller.signal })
      .then(data => { if (!controller.signal.aborted) { setModelOptions(data); setModelID(data.default_model_id); } })
      .catch(error => { if (!controller.signal.aborted) setModelError(workError(error)); })
      .finally(() => { if (!controller.signal.aborted) setModelsLoading(false); });
    return () => controller.abort();
  }, [profileID]);
  const selectedModel = modelOptions?.items.find(model => model.id === modelID);
  const tasks = assistantTasks(selectedAssistant);
  const selectedTask = tasks.find((item) => item.id === taskPresetID);
  const effectiveTeamID = project?.team_id || selectedAssistant?.team_id;
  const writableProjects = (projects as WorkProject[]).filter(
    (p) =>
      p.user?.id === user.id ||
      p.collaborators?.some(
        (member) => member.id === user.id && member.permission === "read_write",
      ),
  );
  useEffect(() => {
    const contextKey = `${selectedProject}:${project?.default_profile_id || ""}`;
    const projectChanged = previousProject.current !== contextKey;
    previousProject.current = contextKey;
    setProfileID((previous) =>
      chooseAssistantID(
        availableProfiles,
        previous,
        project?.default_profile_id,
        projectChanged,
      ),
    );
  }, [availableProfiles, selectedProject, project?.default_profile_id]);
  useEffect(() => {
    setTaskPresetID("");
  }, [profileID]);
  useEffect(() => {
    const controller = new AbortController();
    workRequest<WorkConfig>("/api/v1/users/work/config", {
      signal: controller.signal,
    })
      .then((value) => {
        setConfig(value);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setConfigError(workError(e));
      });
    return () => controller.abort();
  }, []);
  useEffect(() => {
    setVersionIDs([]);
  }, [selectedProject]);
  const skillLoadRef = useRef(0);
  const loadSkills = useCallback(() => {
    const request = ++skillLoadRef.current;
    setSkills([]);
    setSkillsLoading(true);
    setSkillIssues([]);
    const query = profileID ? `?profile_id=${encodeURIComponent(profileID)}` : "";
    void workRequest<{ items: WorkSkill[] }>(`/api/v1/users/work/skills/mentions${query}`)
      .then((data) => { if (request === skillLoadRef.current) setSkills(data.items); })
      .catch((e) => { if (request === skillLoadRef.current) setSkillIssues([workError(e)]); })
      .finally(() => { if (request === skillLoadRef.current) setSkillsLoading(false); });
  }, [profileID]);
  useEffect(() => {
    loadSkills();
    return () => { skillLoadRef.current++; };
  }, [loadSkills]);
  const upload = async (files: FileList | null) => {
    if (!files?.length || uploadBusy.current || submitting) return;
    if (attachments.length + versionIDs.length + files.length > MAX_TASK_UPLOAD_FILES) {
      setError(t("assistantTasks.uploadCountLimit", { count: MAX_TASK_UPLOAD_FILES }));
      if (uploadRef.current) uploadRef.current.value = "";
      return;
    }
    uploadBusy.current = true;
    setUploading(true);
    setError("");
    try {
      let total = attachments.reduce((sum, file) => sum + file.size, 0) + resources.flatMap(resource => resource.versions || []).filter(version => versionIDs.includes(version.id)).reduce((sum, version) => sum + (version.size || 0), 0);
      for (const file of Array.from(files)) {
        const uploaded = await uploadTaskFile(file);
        total += uploaded.size;
        if (total > MAX_TASK_UPLOAD_TOTAL_BYTES) throw new Error(t("assistantTasks.uploadTotalLimit"));
        setAttachments((prev) => [
          ...prev,
          { url: uploaded.accessUrl, filename: uploaded.name, size: uploaded.size },
        ]);
      }
    } catch (e) {
      setError(e instanceof TaskUploadFileTooLargeError ? t("assistantTasks.uploadFileLimit", { size: MAX_TASK_UPLOAD_FILE_SIZE_LABEL }) : workError(e));
    } finally {
      uploadBusy.current = false;
      setUploading(false);
      if (uploadRef.current) uploadRef.current.value = "";
    }
  };
  const submit = async () => {
    if (
      (!content.trim() && !skillMention) ||
      loadedDraftKey !== draftKey ||
      submitting ||
      uploading ||
      uploadBusy.current ||
      !config ||
      config.enabled === false ||
      !profileID ||
      !selectedAssistant ||
      (!!selectedProject && !project) ||
      !selectedModel || modelsLoading ||
      skillsLoading ||
      skillIssues.length > 0
    )
      return;
    setSubmitting(true);
    setError("");
    const profile = availableProfiles.find((p) => p.id === profileID);
    const input: WorkTaskInput = {
      model_id: modelID,
      content: content.trim(),
      project_id: selectedProject || undefined,
      issue_id: issueSource?.issueId,
      branch: project?.repo_url ? branch || undefined : undefined,
      team_id: effectiveTeamID || undefined,
      profile_id: profile?.id,
      task_preset_id: selectedTask?.id,
      resource_version_ids: versionIDs,
      mentioned_skill_id: skillMention?.id,
      attachments: attachments.map(({ url, filename }) => ({ url, filename })),
    };
    const signature = JSON.stringify(input);
    if (requestSignature.current !== signature) {
      requestID.current = workRequestID();
      requestSignature.current = signature;
    }
    input.client_request_id = requestID.current;
    try {
      const task = await workPost<WorkTask>("/api/v1/users/work-tasks", input);
      if (!task.id) throw new Error("服务未返回任务编号，请重试");
      draftSubmitted.current = true;
      try { localStorage.removeItem(draftKey); } catch { /* Optional draft storage. */ }
      setContent("");
      setSkillMention(null);
      reloadProjects();
      reloadUnlinkedTasks();
      reloadHistoricalTasks();
      window.dispatchEvent(
        new CustomEvent("task-library-changed", {
          detail: { projectId: selectedProject, taskId: task.id },
        }),
      );
      navigate(`/console/task/${task.id}`);
    } catch (e) {
      setError(workError(e));
    } finally {
      setSubmitting(false);
    }
  };
  const selectResourceVersion = (resource: WorkResource, version: string) => {
    if (uploadBusy.current) return;
    const resourceVersions = new Set([
      resource.current_version_id,
      ...(resource.versions || []).map((item) => item.id),
    ]);
    const remaining = versionIDs.filter((id) => !resourceVersions.has(id));
    const next = versionIDs.includes(version) ? remaining : [...remaining, version];
    if (next.length + attachments.length > MAX_TASK_UPLOAD_FILES) {
      setError(t("assistantTasks.uploadCountLimit", { count: MAX_TASK_UPLOAD_FILES }));
      return;
    }
    const total = attachments.reduce((sum, file) => sum + file.size, 0) + resources.flatMap(item => item.versions || []).filter(item => next.includes(item.id)).reduce((sum, item) => sum + (item.size || 0), 0);
    if (total > MAX_TASK_UPLOAD_TOTAL_BYTES) { setError(t("assistantTasks.uploadTotalLimit")); return; }
    setVersionIDs(next);
  };
  const selectedContext = [
    ...attachments.map((file, index) => ({
      key: `attachment-${index}`,
      label: file.filename,
      Icon: FileText,
      remove: () =>
        setAttachments((previous) => previous.filter((_, i) => i !== index)),
    })),
    ...resources.flatMap((resource) => {
      const versions = [
        resource.current_version_id,
        ...(resource.versions || []).map((version) => version.id),
      ];
      return versionIDs
        .filter((id) => versions.includes(id))
        .map((id) => ({
          key: `version-${id}`,
          label: `${resource.filename} · ${id === resource.current_version_id ? t("assistantTasks.currentVersion") : t("assistantTasks.fixedVersion")}`,
          Icon: FileText,
          remove: () =>
            setVersionIDs((previous) =>
              previous.filter((version) => version !== id),
            ),
        }));
    }),
  ];
  return (
    <div className="w-full">
      {issueSource && (
        <div className="mb-3 flex min-w-0 items-center gap-2 text-sm text-muted-foreground" title={issueSource.title}>
          <FileText className="size-4 shrink-0" />
          <span className="shrink-0">{t(`consoleProject.issue.actions.${issueSource.intent === "design" ? "startDesign" : "startDevelop"}`)}</span>
          <span className="truncate">{t("assistantTasks.sourceIssue", { title: issueSource.title })}</span>
        </div>
      )}
      {skillIssues.map((issue) => (
        <WorkError key={issue} message={issue} />
      ))}
      <MessageComposerFrame>
        {skillMention && <div className="mb-1"><SkillMentionChip mention={skillMention} disabled={submitting} onRemove={() => setSkillMention(null)} /></div>}
        <MessageComposerTextarea
          ref={contentRef}
          autoFocus
          compact={compact}
          aria-label={t("assistantTasks.goalLabel")}
          value={content}
          disabled={submitting}
          onChange={(e) => {
            setContent(e.target.value);
            requestID.current = workRequestID();
          }}
          onKeyDown={(e) => {
            if (isComposing.current || e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return;
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              void submit();
            }
          }}
          onPaste={(e) => {
            if (!e.clipboardData.files.length) return;
            e.preventDefault();
            void upload(e.clipboardData.files);
          }}
          onCompositionStart={() => { isComposing.current = true; }}
          onCompositionEnd={() => { isComposing.current = false; }}
          placeholder={t("assistantTasks.goalPlaceholder")}
        />
        {selectedContext.length > 0 && (
          <div
            aria-label={t("assistantTasks.selectedContext")}
            className="mt-2 flex max-h-16 flex-wrap gap-1.5 overflow-y-auto overscroll-contain"
          >
            {selectedContext.map((item) => (
              <span
                key={item.key}
                className="flex max-w-full items-center gap-2 rounded-lg bg-muted px-2 py-1 text-xs"
              >
                <item.Icon className="size-3.5 shrink-0" />
                <span className="max-w-48 truncate" title={item.label}>
                  {item.label}
                </span>
                <button
                  type="button"
                  aria-label={t("assistantTasks.removeContext", {
                    name: item.label,
                  })}
                  disabled={submitting}
                  onClick={item.remove}
                >
                  <X className="size-3" />
                </button>
              </span>
            ))}
          </div>
        )}
        <div className="mt-2 flex w-full min-w-0 items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <input
              ref={uploadRef}
              type="file"
              multiple
              className="hidden"
              onChange={(e) => void upload(e.target.files)}
            />
            <WorkContextMenu
              disabled={submitting}
              uploading={uploading}
              projectSelected={!!selectedProject}
              resources={resources}
              resourcesLoading={contextLoading}
              contextError={contextError}
              skills={skills}
              skillsLoading={skillsLoading}
              skillError={skillIssues.join("；")}
              versionIDs={versionIDs}
              onUpload={() => uploadRef.current?.click()}
              onSkillsOpen={() => { loadSkills(); reloadFiles(); }}
              onSkillChange={(skill) => setSkillMention(skillToMention(skill))}
              onVersionChange={selectResourceVersion}
            />
          </div>
          <div className="flex items-center gap-2">
            <ModelSelect compact models={modelOptions?.items || []} selectedModelId={modelID} selectedModel={selectedModel} setSelectedModelId={setModelID} subscription={subscription} disabled={submitting || uploading || !profileID} loading={modelsLoading} error={modelError} />
            <Button
              aria-label={t("assistantTasks.start")}
              disabled={
                (!content.trim() && !skillMention) ||
                loadedDraftKey !== draftKey ||
                submitting ||
                skillsLoading ||
                skillIssues.length > 0 ||
                uploading ||
                !config ||
                config.enabled === false ||
                !profileID ||
                !selectedAssistant
                || (!!selectedProject && !project)
                || !selectedModel || modelsLoading
              }
              onClick={() => void submit()}
              className="size-8 shrink-0 rounded-lg p-0 max-sm:size-11"
            >
              {submitting ? (
                <Loader2 className="size-4 animate-spin" />
              ) : (
                <ArrowUp className="size-4" />
              )}
            </Button>
          </div>
        </div>
        <div className="-mx-3 -mb-3 mt-3 flex flex-wrap items-center gap-1 rounded-b-2xl border-t bg-muted/40 px-3 py-1.5 sm:-mx-3.5 sm:-mb-3.5 sm:px-3.5">
          <WorkSelect
            label={t("assistantTasks.selectAssistant")}
            placeholder={t("assistantTasks.selectAssistant")}
            searchPlaceholder={t("assistantTasks.searchAssistants")}
            emptyText={t("assistantTasks.noAssistants")}
            value={profileID}
            icon={Bot}
            disabled={submitting || !config}
            options={availableProfiles.map((p) => ({
              id: p.id,
              label: p.name,
              description:
                p.description ||
                config?.teams?.find((team) => team.id === p.team_id)?.name,
            }))}
            onChange={(id) => {
              setContent((previous) =>
                applyTaskDescription(previous, selectedTask, undefined),
              );
              setProfileID(id);
              setTaskPresetID("");
            }}
          />
          <WorkSelect
            label={t("assistantTasks.selectProject")}
            placeholder={t("assistantTasks.optionalProject")}
            searchPlaceholder={t("assistantTasks.searchProjects")}
            emptyText={t("assistantTasks.noProjects")}
            value={selectedProject}
            icon={FolderOpen}
            disabled={submitting || uploading || !!issueSource}
            options={[
              { id: "", label: t("assistantTasks.noProject") },
              ...writableProjects.map((p) => ({
                id: p.id!,
                label: p.name || "",
                description: p.repo_url || p.description,
                icon: p.repo_url ? GitBranch : FolderOpen,
              })),
            ]}
            onChange={setSelectedProject}
            createLabel={t("assistantTasks.newProject")}
            onCreate={() => setProjectDialogOpen(true)}
          />
          {project?.repo_url && <WorkSelect
            label={t("assistantTasks.selectBranch")}
            placeholder={t("assistantTasks.defaultBranch")}
            searchPlaceholder={t("assistantTasks.searchBranch")}
            emptyText={branchesLoading ? t("common.loading") : t("assistantTasks.searchBranch")}
            value={branch}
            icon={GitBranch}
            disabled={submitting}
            options={[{ id: "", label: t("assistantTasks.defaultBranch") }, ...Array.from(new Set([...branches, ...(branch ? [branch] : [])])).map(name => ({ id: name, label: name }))]}
            onChange={name => setBranchSelection({ project: selectedProject, name })}
            customLabel={name => t("assistantTasks.useBranch", { name })}
            onLoadMore={hasMoreBranches && !branchesLoadingMore ? loadMoreBranches : undefined}
            loadMoreLabel={t("assistantTasks.loadMoreBranches")}
          />}
        </div>
      </MessageComposerFrame>
      {/* Leave room for focus rings and the button's pressed translation. */}
      <div className="mt-8 flex max-h-24 flex-wrap justify-center gap-2 overflow-y-auto overscroll-contain p-1">
        {tasks.map((item) => {
          const Icon = taskPresetIcons[item.icon] || Sparkles;
          return (
            <Button
              key={item.id}
              variant="outline"
              size="sm"
              aria-pressed={taskPresetID === item.id}
              title={item.description}
              disabled={submitting}
              className={cn(
                "rounded-xl bg-card",
                taskPresetID === item.id &&
                  "border-primary/30 bg-accent text-primary",
              )}
              onClick={() => {
                const next = selectedTask?.id === item.id ? undefined : item;
                setTaskPresetID(next?.id || "");
                setContent((previous) =>
                  applyTaskDescription(previous, selectedTask, next),
                );
              }}
            >
              <Icon className="size-3.5" />
              {item.name}
            </Button>
          );
        })}
      </div>
      {(error || configError) && (
        <div className="mt-4">
          <WorkError message={error || configError} />
        </div>
      )}
      {config?.enabled === false && (
        <p
          role="status"
          className="mt-4 text-center text-sm text-muted-foreground"
        >
          工作任务尚未启用，请联系管理员完成执行环境配置。
        </p>
      )}
      {config?.enabled !== false && config && !availableProfiles.length && (
        <p
          role="status"
          className="mt-4 text-center text-sm text-muted-foreground"
        >
          {t("assistantTasks.noAssistants")}
        </p>
      )}
      {selectedAssistant && !tasks.length && (
        <p
          role="status"
          className="mt-4 text-center text-sm text-muted-foreground"
        >
          {t("assistantTasks.noAssistantTasks")}
        </p>
      )}
      <AddProjectDialog
        open={projectDialogOpen}
        onOpenChange={setProjectDialogOpen}
        navigateOnSuccess={false}
        teamID={effectiveTeamID}
        onCreated={(id) => {
          setSelectedProject(id);
          reloadProjects();
        }}
      />
    </div>
  );
}
