import type { WorkProject } from "./work-api";

type ProjectSettingsValues = {
  name: string;
  rules: string;
  profileID: string;
  collaborators: Record<string, "read_only" | "read_write">;
};

export function canManageProjectMembers(project: WorkProject, userID?: string) {
  return Boolean(userID && project.user?.id === userID);
}

export function projectSettingsPayload(
  project: WorkProject,
  userID: string | undefined,
  values: ProjectSettingsValues,
) {
  return {
    name: values.name.trim(),
    rules: values.rules,
    default_profile_id: values.profileID || "00000000-0000-0000-0000-000000000000",
    ...(canManageProjectMembers(project, userID)
      ? {
          collaborators: Object.entries(values.collaborators).map(
            ([user_id, permission]) => ({ user_id, permission }),
          ),
        }
      : {}),
  };
}
