import type {
  DomainProject,
  DomainModel,
  DomainProjectTask,
  DomainTeamMember,
  DomainTeamUser,
} from "@/api/Api";
import { v4 as uuidv4 } from "uuid";
import { loginRedirectForPath } from "@/utils/login-redirect";

export type TaskPresetIcon =
  | "sparkles"
  | "chart-column"
  | "file-text"
  | "presentation"
  | "code";
export type WorkTaskPreset = {
  id: string;
  name: string;
  description: string;
  icon: TaskPresetIcon;
  enabled: boolean;
};
export type WorkProfile = {
  id?: string;
  team_id: string;
  name: string;
  description: string;
  instructions: string;
  task_presets: WorkTaskPreset[];
  is_default: boolean;
  enabled: boolean;
  model_id: string;
  host_id: string;
  image_id: string;
  cli_name: "opencode" | "claude" | "codex";
  required_skill_ids: string[];
  mentionable_skill_ids?: string[];
  core: number;
  memory: number;
};
export type WorkProject = DomainProject & {
  project_kind?: "business" | "code";
  team_id?: string;
  rules?: string;
  default_skill_ids?: string[];
  default_profile_id?: string;
};
export type WorkConfig = {
  enabled?: boolean;
  offline?: boolean;
  profiles?: {
    id: string;
    name: string;
    description?: string;
    task_presets?: WorkTaskPreset[];
    team_id?: string;
    is_default?: boolean;
    enabled?: boolean;
    required_skill_ids?: string[];
  }[];
  team_ids?: string[];
  teams?: { id: string; name: string }[];
};
export type WorkMetadata = {
  profile_id?: string;
  assistant_name?: string;
  task_preset?: WorkTaskPreset;
  business_status?: string;
  output_requirements?: string;
  skill_pins?: {
    skill_id: string;
    version_id?: string;
    version?: string;
    content_hash?: string;
  }[];
  input_resources?: {
    resource_id: string;
    version_id: string;
    filename?: string;
    content_hash?: string;
  }[];
};
export type WorkTask = DomainProjectTask & { work?: WorkMetadata };
export type ResourceVersion = {
  id: string;
  resource_id?: string;
  filename: string;
  size?: number;
  sha256?: string;
  created_at?: number | string;
};
export type WorkResource = {
  id: string;
  owner_id?: string;
  project_id?: string;
  filename: string;
  current_version_id: string;
  versions?: ResourceVersion[];
};
export type ArtifactVersion = {
  id: string;
  artifact_id?: string;
  version: number;
  filename: string;
  mime_type?: string;
  size?: number;
  sha256?: string;
  validation_status?: string;
  preview_status?: string;
  created_at?: number | string;
};
export type WorkArtifact = {
  id: string;
  task_id: string;
  project_id?: string;
  title?: string;
  filename: string;
  current_version_id: string;
  validation_status?: string;
  preview_status?: string;
  versions?: ArtifactVersion[];
  current_version?: ArtifactVersion;
  version_count?: number;
  can_edit?: boolean;
};
export type WorkArtifactPage = {
  items: WorkArtifact[];
  has_more: boolean;
  next_cursor?: string;
};
export type WorkTaskInput = {
  branch?: string;
  model_id?: string;
  mentioned_skill_id?: string;
  content: string;
  project_id?: string;
  issue_id?: string;
  team_id?: string;
  profile_id?: string;
  task_preset_id?: string;
  resource_version_ids?: string[];
  skill_ids?: string[];
  skill_selection?: { ids: string[] };
  attachments?: { url: string; filename: string }[];
  output_requirements?: string;
  client_request_id?: string;
};
export type WorkModelOptions = { items: DomainModel[]; default_model_id: string };
export type WorkSkillVersion = {
  id: string;
  version: string;
  sha256?: string;
  created_at?: number | string;
};
export type WorkSkill = {
  origins?: string[];
  id: string;
  name: string;
  description: string;
  scope: { type: string; id?: string };
  publication_state: "draft" | "trial" | "review" | "published";
  enabled: boolean;
  active_version_id?: string;
  versions: WorkSkillVersion[];
  content?: string;
  tags?: string[];
  source_type?: string;
  source_label?: string;
  source_url?: string;
  source_version?: string;
};

export type WorkTaskList = {
  items: WorkTask[];
  page?: number;
  has_more?: boolean;
};

export class WorkApiError extends Error {
  status: number;
  requiresBinding = false;
  constructor(message: string, status: number) {
    super(message);
    this.name = "WorkApiError";
    this.status = status;
  }
}

export async function workRequest<T>(
  path: string,
  init?: RequestInit,
): Promise<T> {
  const headers = new Headers(init?.headers);
  if (
    init?.body &&
    !(init.body instanceof FormData) &&
    !headers.has("Content-Type")
  )
    headers.set("Content-Type", "application/json");
  const response = await fetch(path, {
    ...init,
    credentials: "include",
    headers,
  });
  if (response.status === 401) {
    const loginPath = loginRedirectForPath(window.location.pathname);
    if (loginPath) window.location.assign(loginPath);
    throw new WorkApiError("登录已过期，请重新登录", 401);
  }
  let body: { code?: number; message?: string; data?: T };
  try {
    body = await response.json();
  } catch {
    throw new WorkApiError(`请求失败（${response.status}）`, response.status);
  }
  if (!response.ok || body.code !== 0) {
    const error = new WorkApiError(
      body.message || `请求失败（${response.status}）`,
      response.status,
    );
    error.requiresBinding = Boolean(
      (body.data as { requires_binding?: boolean } | undefined)
        ?.requires_binding,
    );
    throw error;
  }
  return body.data as T;
}
export const workPost = <T>(path: string, value: unknown) =>
  workRequest<T>(path, { method: "POST", body: JSON.stringify(value) });
export function workItems<T>(
  data: T[] | { items?: T[]; tasks?: T[]; projects?: T[] } | null,
): T[] {
  return Array.isArray(data)
    ? data
    : (data?.items ?? data?.tasks ?? data?.projects ?? []);
}
export const workError = (error: unknown) =>
  error instanceof Error ? error.message : "操作失败，请重试";
// Management uses a separate team session. Never infer its selected team from
// an employee session or from a persisted browser team ID.
export function workManagerTeams(
  session: DomainTeamUser | null,
): DomainTeamMember[] {
  if (!session?.team?.id || !session.user?.id) return [];
  return [
    {
      team_id: session.team.id,
      team_name: session.team.name,
      team_role: "admin" as DomainTeamMember["team_role"],
      user_id: session.user.id,
    },
  ];
}
export const workStatusLabels: Record<string, string> = {
  queued: "排队中",
  preparing: "准备中",
  running: "执行中",
  waiting_input: "等待补充",
  waiting_approval: "等待确认",
  waiting_confirmation: "等待确认",
  validating: "检查成果中",
  completed: "已交付",
  delivered: "已交付",
  failed: "需要处理",
  pending: "待交付",
  processing: "进行中",
  finished: "已结束",
  error: "需要处理",
};
export function workTaskDisplay(task: WorkTask) {
  const status = task.work?.business_status || task.status || "pending";
  return {
    label: workStatusLabels[status] || "状态待确认",
    completed: ["delivered", "completed", "finished"].includes(status),
  };
}
export function workRequestID() {
  return globalThis.crypto?.randomUUID?.() ?? uuidv4();
}
export const canEditArtifact = (artifact: WorkArtifact, taskId?: string) =>
  artifact.can_edit ?? Boolean(taskId && artifact.task_id === taskId);
export const artifactURL = (
  id: string,
  action: "preview" | "download",
  version?: string,
) =>
  `/api/v1/users/artifacts/${encodeURIComponent(id)}/${action}${version ? `?version_id=${encodeURIComponent(version)}` : ""}`;
export function workTime(value?: number | string) {
  if (!value) return "";
  const date = new Date(
    typeof value === "number" && value < 100000000000 ? value * 1000 : value,
  );
  return Number.isNaN(date.getTime())
    ? ""
    : date.toLocaleString("zh-CN", {
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
      });
}
export function workSize(value?: number) {
  return value === undefined
    ? ""
    : value < 1048576
      ? `${Math.ceil(value / 1024)} KB`
      : `${(value / 1048576).toFixed(1)} MB`;
}
