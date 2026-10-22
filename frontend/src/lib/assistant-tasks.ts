import type {
  WorkConfig,
  WorkProject,
  WorkTask,
  WorkTaskPreset,
} from "./work-api";

export function chooseAssistantID(
  profiles: NonNullable<WorkConfig["profiles"]>,
  previous: string,
  projectDefault?: string,
  contextChanged = false,
): string {
  if (!contextChanged && profiles.some((p) => p.id === previous))
    return previous;
  if (projectDefault)
    return profiles.some((p) => p.id === projectDefault) ? projectDefault : "";
  return profiles.find((p) => p.is_default)?.id || profiles[0]?.id || "";
}

export function availableAssistants(
  config: WorkConfig | null,
  project?: WorkProject,
) {
  return (config?.profiles || []).filter(
    (assistant) =>
      assistant.enabled !== false &&
      (!project?.team_id || assistant.team_id === project.team_id),
  );
}

export function assistantTasks(
  assistant?: NonNullable<WorkConfig["profiles"]>[number],
) {
  return (assistant?.task_presets || []).filter((task) => task.enabled);
}

// Preserve an edited user goal when changing an assistant or task. Untouched
// preset text follows the newly selected task instead of becoming stale.
export function applyTaskDescription(
  content: string,
  previous?: WorkTaskPreset,
  next?: WorkTaskPreset,
) {
  return !content.trim() || content.trim() === previous?.description.trim()
    ? next?.description || ""
    : content;
}

export function normalizeTaskProjectID(value?: string | null) {
  const id = value?.trim() || "";
  return id === "00000000-0000-0000-0000-000000000000" ? "" : id;
}

export function taskProjectID(task: WorkTask) {
  return normalizeTaskProjectID(task.project_id);
}

export function taskLibraryURL({
  page = 1,
  limit = 20,
  projectId,
  unlinked = false,
}: {
  page?: number;
  limit?: number;
  projectId?: string;
  unlinked?: boolean;
} = {}) {
  const params = new URLSearchParams({
    page: String(page),
    limit: String(limit),
  });
  if (projectId) params.set("project_id", projectId);
  else if (unlinked) params.set("unlinked", "true");
  return `/api/v1/users/task-library?${params}`;
}
