import type { WorkProfile } from "./work-api";

export type ProfileTab = "basic" | "tasks" | "execution";
export type ProfileIssue = {
  tab: ProfileTab;
  field: keyof WorkProfile;
  key: string;
  taskId?: string;
  part?: "name" | "description";
};

export function profileIssues(form: WorkProfile): ProfileIssue[] {
  const issues: ProfileIssue[] = [];
  const bytes = (value: string) =>
    new TextEncoder().encode(value.trim()).length;
  const add = (tab: ProfileTab, field: keyof WorkProfile, key: string) =>
    issues.push({ tab, field, key });
  if (!form.name.trim()) add("basic", "name", "nameRequired");
  else if (bytes(form.name) > 120) add("basic", "name", "nameTooLong");
  if (new TextEncoder().encode(form.description).length > 4096)
    add("basic", "description", "textTooLong");
  if (new TextEncoder().encode(form.instructions).length > 8192)
    add("basic", "instructions", "textTooLong");
  if (!form.task_presets.length) add("tasks", "task_presets", "tasksRequired");
  if (form.task_presets.length > 32)
    add("tasks", "task_presets", "tooManyTasks");
  for (const task of form.task_presets) {
    for (const part of ["name", "description"] as const) {
      const length = bytes(task[part]);
      if (!length || length > (part === "name" ? 120 : 8192)) {
        issues.push({
          tab: "tasks",
          field: "task_presets",
          taskId: task.id,
          part,
          key: !length
            ? part === "name"
              ? "taskNameRequired"
              : "taskDescriptionRequired"
            : "textTooLong",
        });
      }
    }
  }
  if (form.enabled && !form.task_presets.some((task) => task.enabled))
    add("tasks", "task_presets", "visibleTaskRequired");
  if (
    form.task_presets.reduce(
      (size, task) => size + bytes(task.description),
      0,
    ) > 65536
  )
    add("tasks", "task_presets", "tasksTooLong");
  if (!form.model_id) add("execution", "model_id", "modelRequired");
  if (!form.image_id) add("execution", "image_id", "imageRequired");
  const ranges = [
    ["core", form.core, 1, 32],
    ["memory", form.memory / 1073741824, 1, 128],
  ] as const;
  for (const [field, value, min, max] of ranges)
    if (!Number.isInteger(value) || value < min || value > max)
      add("execution", field, "invalidCapacity");
  if (form.required_skill_ids.length > 32)
    add("execution", "required_skill_ids", "tooManySkills");
  return issues;
}
