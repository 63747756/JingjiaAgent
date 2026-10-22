import assert from "node:assert/strict";
import test from "node:test";
import { profileIssues } from "../src/lib/assistant-profile-form.ts";
import type { WorkProfile } from "../src/lib/work-api.ts";

const profile = (): WorkProfile => ({
  team_id: "team",
  name: "经营助手",
  description: "",
  instructions: "",
  task_presets: [
    {
      id: "review",
      name: "经营复盘",
      description: "核对数据口径后完成汇报。",
      icon: "chart-column",
      enabled: true,
    },
  ],
  is_default: false,
  enabled: true,
  model_id: "model",
  host_id: "host",
  image_id: "image",
  cli_name: "opencode",
  required_skill_ids: [],
  core: 2,
  memory: 8 * 1073741824,
});

test("incomplete configuration identifies the correct tab and individual preset", () => {
  const form = profile();
  form.task_presets.push({
    id: "new",
    name: "",
    description: "",
    icon: "sparkles",
    enabled: true,
  });
  form.model_id = "";
  const issues = profileIssues(form);
  assert.deepEqual(
    issues.map(({ tab, taskId, part, key }) => ({ tab, taskId, part, key })),
    [
      { tab: "tasks", taskId: "new", part: "name", key: "taskNameRequired" },
      {
        tab: "tasks",
        taskId: "new",
        part: "description",
        key: "taskDescriptionRequired",
      },
      {
        tab: "execution",
        taskId: undefined,
        part: undefined,
        key: "modelRequired",
      },
    ],
  );
});

test("hidden presets remain configurable but enabled assistants require a visible task", () => {
  const form = profile();
  form.task_presets[0].enabled = false;
  assert.equal(profileIssues(form)[0].key, "visibleTaskRequired");
  form.enabled = false;
  assert.deepEqual(profileIssues(form), []);
  form.task_presets = [];
  assert.equal(profileIssues(form)[0].key, "tasksRequired");
});

test("Chinese content respects server byte limits and total task description size", () => {
  const form = profile();
  form.name = "中".repeat(41);
  assert.equal(profileIssues(form)[0].key, "nameTooLong");
  form.name = "经营助手";
  form.task_presets = Array.from({ length: 9 }, (_, index) => ({
    id: `task-${index}`,
    name: `任务${index}`,
    description: "中".repeat(2700),
    icon: "chart-column",
    enabled: true,
  }));
  assert.equal(profileIssues(form)[0].key, "tasksTooLong");
});

test("advanced capacity errors remain actionable even while the section is collapsed", () => {
  const form = profile();
  form.core = 0;
  form.memory = 0;
  assert.deepEqual(
    profileIssues(form).map(({ tab, field }) => ({ tab, field })),
    [
      { tab: "execution", field: "core" },
      { tab: "execution", field: "memory" },
    ],
  );
});

test("automatic host selection is a valid assistant configuration", () => {
 const form=profile(); form.host_id=""; assert.deepEqual(profileIssues(form), []);
});
