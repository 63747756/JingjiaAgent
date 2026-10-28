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
import { MAX_TASK_CONTENT_LENGTH } from "../task/task-content-limit";
import { createLongContentFileName, createLongContentTextFile } from "../task/task-long-content";
import { TaskLongContentDialog } from "../task/task-long-content-dialog";
import { toast } from "sonner";

type TaskDraftAttachment = { url: string; filename: string; size: number };
type TaskDraft = {
  content?: string;
  skillMention?: SkillMention;
  projectId?: string;
  branchSelection?: { project: string; name: string };
  attachments?: TaskDraftAttachment[];
};

function readTaskDraft(key: string, projectId: string): TaskDraft | null {
  try {
    const draft = parseTaskDraft<TaskDraft>(localStorage.getItem(key), projectId);
    if (!draft) return null;
    return { ...draft, attachments: Array.isArray(draft.attachments) ? draft.attachments.filter(file =>
      file && typeof file.url === "string" && !!file.url && typeof file.filename === "string" && !!file.filename
      && typeof file.size === "number" && Number.isFinite(file.size) && file.size >= 0) : [] };
  } catch {
    return null;
  }
}

function writeTaskDraft(key: string, draft: TaskDraft, preserveEmpty = false): boolean {
  try {
    if (draft.content || draft.skillMention || draft.branchSelection?.name || draft.attachments?.length || preserveEmpty) {
      localStorage.setItem(key, JSON.stringify(draft));
    } else {
      localStorage.removeItem(key);
    }
    return true;
  } catch {
    return false;
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
  const currentContentRef = useRef(content);
  currentContentRef.current = content;
  const [attachments, setAttachments] = useState<TaskDraftAttachment[]>(initialDraft?.attachments || []);
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
  const mountedRef = useRef(false);
  const draftScopeRef = useRef({ key: draftKey });
  if (draftScopeRef.current.key !== draftKey) draftScopeRef.current = { key: draftKey };
  const [isComposing, setIsComposing] = useState(false);
  const [longContentDraft, setLongContentDraft] = useState<{ content: string; filename: string; draftKey: string } | null>(null);
  const [longContentConverting, setLongContentConverting] = useState(false);
  const [longContentPromptSuppressed, setLongContentPromptSuppressed] = useState(false);
  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);
  const { resources, loading: contextLoading, error: contextError, reload: reloadFiles } = useWorkFiles(selectedProject);
  useEffect(() => {
    if (!user.id || draftSubmitted.current) return;
    if (loadedDraftKey !== draftKey) {
      const saved = readTaskDraft(draftKey, selectedProject);
      setContent(initialTaskContent(saved, initialContent));
      setSkillMention(saved?.skillMention || null);
      setBranchSelection(saved?.branchSelection || { project: "", name: "" });
      setAttachments(saved?.attachments || []);
      setLongContentDraft(null);
      setLongContentConverting(false);
      setLongContentPromptSuppressed(false);
      uploadBusy.current = false;
      setUploading(false);
      // Commit the key with the restored input. Replayed effects must not save
      // the previous project's input over the destination project's draft.
      setLoadedDraftKey(draftKey);
      return;
    }
    writeTaskDraft(draftKey, { content, skillMention: skillMention || undefined, projectId: selectedProject, branchSelection, attachments }, !!issueSource);
  }, [content, skillMention, draftKey, loadedDraftKey, initialContent, user.id, issueSource, selectedProject, branchSelection, attachments]);
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
  const uploadBusy = useRef(false);
  const uploadRef = useRef<HTMLInputElement>(null);
  const contentRef = useRef<HTMLTextAreaElement>(null);
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
  const inputLocked = submitting || longContentConverting;
  const contentTooLong = content.length > MAX_TASK_CONTENT_LENGTH;
  const handleContentChange = (nextContent: string) => {
    currentContentRef.current = nextContent;
    setContent(nextContent);
    requestID.current = workRequestID();
  };
  const openLongContentDialog = useCallback(() => {
    if (content.length <= MAX_TASK_CONTENT_LENGTH || loadedDraftKey !== draftKey || !user.id || submitting || uploadBusy.current) return;
    if (attachments.length + versionIDs.length >= MAX_TASK_UPLOAD_FILES) {
      setError(t("assistantTasks.uploadCountLimit", { count: MAX_TASK_UPLOAD_FILES }));
      setLongContentPromptSuppressed(true);
      return;
    }
    setLongContentDraft({ content, filename: createLongContentFileName(new Date()).replace(/\.txt$/, `-${workRequestID()}.txt`), draftKey });
  }, [content, loadedDraftKey, draftKey, user.id, submitting, attachments.length, versionIDs.length, t]);
  const handleLongContentDialogOpenChange = (open: boolean) => {
    if (open || longContentConverting || uploadBusy.current) return;
    setLongContentDraft(null);
    setLongContentPromptSuppressed(true);
  };
  const handleManualLongContentConversion = () => {
    openLongContentDialog();
  };
  useEffect(() => {
    if (content.length <= MAX_TASK_CONTENT_LENGTH) {
      setLongContentPromptSuppressed(false);
      return;
    }
    if (isComposing || longContentPromptSuppressed || longContentDraft || inputLocked || uploading || loadedDraftKey !== draftKey) return;
    openLongContentDialog();
  }, [content.length, isComposing, longContentPromptSuppressed, longContentDraft, inputLocked, uploading, loadedDraftKey, draftKey, openLongContentDialog]);
  const handleConfirmLongContentConversion = async () => {
    if (!longContentDraft || longContentDraft.draftKey !== draftKey || loadedDraftKey !== draftKey || inputLocked || uploadBusy.current) return;
    if (attachments.length + versionIDs.length >= MAX_TASK_UPLOAD_FILES) {
      setError(t("assistantTasks.uploadCountLimit", { count: MAX_TASK_UPLOAD_FILES }));
      return;
    }
    const scope = draftScopeRef.current;
    const stillCurrent = () => mountedRef.current && draftScopeRef.current === scope;
    uploadBusy.current = true;
    setLongContentConverting(true);
    setError("");
    try {
      const file = createLongContentTextFile(longContentDraft.content, longContentDraft.filename);
      const total = attachments.reduce((sum, item) => sum + item.size, 0) + resources.flatMap(resource => resource.versions || []).filter(version => versionIDs.includes(version.id)).reduce((sum, version) => sum + (version.size || 0), 0);
      if (total + file.size > MAX_TASK_UPLOAD_TOTAL_BYTES) throw new Error(t("assistantTasks.uploadTotalLimit"));
      const uploaded = await uploadTaskFile(file);
      if (!stillCurrent()) return;
      if (total + uploaded.size > MAX_TASK_UPLOAD_TOTAL_BYTES) throw new Error(t("assistantTasks.uploadTotalLimit"));
      const nextAttachments = [...attachments, { url: uploaded.accessUrl, filename: uploaded.name, size: uploaded.size }];
      const nextContent = currentContentRef.current === longContentDraft.content ? "" : currentContentRef.current;
      // Preserve the uploaded file reference before replacing the durable text draft.
      if (!writeTaskDraft(draftKey, { content: nextContent, skillMention: skillMention || undefined, projectId: selectedProject, branchSelection, attachments: nextAttachments }, !!issueSource)) {
        throw new Error(t("taskDetail.chat.longContent.convertFailed"));
      }
      setAttachments(nextAttachments);
      currentContentRef.current = nextContent;
      setContent(nextContent);
      setLongContentDraft(null);
      setLongContentPromptSuppressed(false);
      toast.success(t("taskDetail.chat.longContent.addDescription"));
    } catch (cause) {
      if (stillCurrent()) {
        const message = cause instanceof TaskUploadFileTooLargeError ? t("assistantTasks.uploadFileLimit", { size: MAX_TASK_UPLOAD_FILE_SIZE_LABEL }) : workError(cause);
        setError(message);
        toast.error(message);
      }
    } finally {
      if (stillCurrent()) {
        uploadBusy.current = false;
        setLongContentConverting(false);
      }
    }
  };
  const upload = async (files: FileList | null) => {
    if (!files?.length || uploadBusy.current || inputLocked || loadedDraftKey !== draftKey) return;
    if (attachments.length + versionIDs.length + files.length > MAX_TASK_UPLOAD_FILES) {
      setError(t("assistantTasks.uploadCountLimit", { count: MAX_TASK_UPLOAD_FILES }));
      if (uploadRef.current) uploadRef.current.value = "";
      return;
    }
    uploadBusy.current = true;
    setUploading(true);
    setError("");
    const scope = draftScopeRef.current;
    const stillCurrent = () => mountedRef.current && draftScopeRef.current === scope;
    try {
      let total = attachments.reduce((sum, file) => sum + file.size, 0) + resources.flatMap(resource => resource.versions || []).filter(version => versionIDs.includes(version.id)).reduce((sum, version) => sum + (version.size || 0), 0);
      for (const file of Array.from(files)) {
        const uploaded = await uploadTaskFile(file);
        if (!stillCurrent()) return;
        total += uploaded.size;
        if (total > MAX_TASK_UPLOAD_TOTAL_BYTES) throw new Error(t("assistantTasks.uploadTotalLimit"));
        setAttachments((prev) => [
          ...prev,
          { url: uploaded.accessUrl, filename: uploaded.name, size: uploaded.size },
        ]);
      }
    } catch (e) {
      if (stillCurrent()) setError(e instanceof TaskUploadFileTooLargeError ? t("assistantTasks.uploadFileLimit", { size: MAX_TASK_UPLOAD_FILE_SIZE_LABEL }) : workError(e));
    } finally {
      if (stillCurrent()) {
        uploadBusy.current = false;
        setUploading(false);
        if (uploadRef.current) uploadRef.current.value = "";
      }
    }
  };
  const submit = async () => {
    if (content.length > MAX_TASK_CONTENT_LENGTH) {
      setError(t("taskWorkflow.input.contentTooLong", { maxCount: MAX_TASK_CONTENT_LENGTH }));
      return;
    }
    if (
      (!content.trim() && !skillMention) ||
      loadedDraftKey !== draftKey ||
      inputLocked ||
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
        {skillMention && <div className="mb-1"><SkillMentionChip mention={skillMention} disabled={inputLocked} onRemove={() => setSkillMention(null)} /></div>}
        <MessageComposerTextarea
          ref={contentRef}
          autoFocus
          compact={compact}
          aria-label={t("assistantTasks.goalLabel")}
          value={content}
          disabled={inputLocked}
          onChange={(e) => handleContentChange(e.target.value)}
          onKeyDown={(e) => {
            if (isComposing || e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return;
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
          onCompositionStart={() => setIsComposing(true)}
          onCompositionEnd={() => setIsComposing(false)}
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
                  disabled={inputLocked}
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
              disabled={inputLocked}
              uploading={uploading || longContentConverting}
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
            <ModelSelect compact models={modelOptions?.items || []} selectedModelId={modelID} selectedModel={selectedModel} setSelectedModelId={setModelID} subscription={subscription} disabled={inputLocked || uploading || !profileID} loading={modelsLoading} error={modelError} />
            <Button
              aria-label={t("assistantTasks.start")}
              disabled={
                (!content.trim() && !skillMention) ||
                loadedDraftKey !== draftKey ||
                inputLocked || contentTooLong ||
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
            disabled={inputLocked || !config}
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
            disabled={inputLocked || uploading || !!issueSource}
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
            disabled={inputLocked}
            options={[{ id: "", label: t("assistantTasks.defaultBranch") }, ...Array.from(new Set([...branches, ...(branch ? [branch] : [])])).map(name => ({ id: name, label: name }))]}
            onChange={name => setBranchSelection({ project: selectedProject, name })}
            customLabel={name => t("assistantTasks.useBranch", { name })}
            onLoadMore={hasMoreBranches && !branchesLoadingMore ? loadMoreBranches : undefined}
            loadMoreLabel={t("assistantTasks.loadMoreBranches")}
          />}
        </div>
      </MessageComposerFrame>
      {contentTooLong && (
        <div className="mt-1 flex items-center justify-between gap-2 px-1 text-xs text-destructive">
          <span>{t("taskDetail.chat.contentTooLongInline", { overCount: content.length - MAX_TASK_CONTENT_LENGTH, maxCount: MAX_TASK_CONTENT_LENGTH })}</span>
          <Button type="button" variant="ghost" size="xs" className="shrink-0 text-destructive hover:text-destructive"
            disabled={inputLocked || uploading || loadedDraftKey !== draftKey} onClick={handleManualLongContentConversion}>
            <FileText className="size-3.5" />{t("taskDetail.chat.longContent.manualConvert")}
          </Button>
        </div>
      )}
      <TaskLongContentDialog open={!!longContentDraft} characterCount={longContentDraft?.content.length ?? content.length}
        filename={longContentDraft?.filename ?? ""} converting={longContentConverting}
        onOpenChange={handleLongContentDialogOpenChange} onConfirm={handleConfirmLongContentConversion} />
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
              disabled={inputLocked}
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
