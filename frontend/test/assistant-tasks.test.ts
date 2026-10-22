import assert from "node:assert/strict";
import test from "node:test";
import { chooseAssistantID, availableAssistants, assistantTasks, applyTaskDescription, taskProjectID, taskLibraryURL } from "../src/lib/assistant-tasks.ts";
import type { WorkConfig, WorkTaskPreset } from "../src/lib/work-api.ts";

const custom: WorkTaskPreset = { id: "monthly-review", name: "月度经营复盘", description: "核对各部门业绩并制作经营汇报。", icon: "chart-column", enabled: true };
const other: WorkTaskPreset = { id: "repair-analysis", name: "维修异常分析", description: "分析维修案例并列出待确认问题。", icon: "file-text", enabled: true };
const config: WorkConfig = { profiles: [
  { id: "a", name: "经营助手", team_id: "team-a", task_presets: [custom, { ...other, enabled: false }] },
  { id: "b", name: "维修助手", team_id: "team-b", task_presets: [other] },
  { id: "disabled", name: "停用助手", enabled: false, team_id: "team-a", task_presets: [other] },
] };

test("assistant tasks come from the selected assistant, including custom names, and exclude disabled tasks", () => {
  assert.deepEqual(assistantTasks(config.profiles![0]), [custom]);
  assert.deepEqual(assistantTasks(config.profiles![1]), [other]);
  assert.deepEqual(assistantTasks(undefined), []);
  assert.deepEqual(assistantTasks({ id: "empty", name: "空助手" }), []);
});

test("both repository and name-only projects can use authorized assistants", () => {
  assert.deepEqual(availableAssistants(config).map((p) => p.id), ["a", "b"]);
  assert.deepEqual(availableAssistants(config, { id: "repo", project_kind: "code", team_id: "team-a" }).map((p) => p.id), ["a"]);
  assert.deepEqual(availableAssistants(config, { id: "named", project_kind: "business", team_id: "team-b" }).map((p) => p.id), ["b"]);
});

test("task selection fills descriptions and preserves an edited user request when switching", () => {
  assert.equal(applyTaskDescription("", undefined, custom), custom.description);
  assert.equal(applyTaskDescription(custom.description, custom, other), other.description);
  assert.equal(applyTaskDescription("统计第三季度，输出两页摘要", custom, other), "统计第三季度，输出两页摘要");
  assert.equal(applyTaskDescription(custom.description, custom, undefined), "");
});

test("project grouping supports new assistant tasks and historical coding tasks without mixing independent tasks", () => {
  assert.equal(taskProjectID({ project_id: "new-project", work: {} }), "new-project");
  assert.equal(taskProjectID({ type: "develop", project_id: "old-project" }), "old-project");
  assert.equal(taskProjectID({ type: "work" }), "");
  assert.equal(taskProjectID({ project_id: null }), "");
  assert.equal(taskProjectID({ project_id: "00000000-0000-0000-0000-000000000000" }), "");
  assert.equal(taskLibraryURL({ unlinked: true }), "/api/v1/users/task-library?page=1&limit=20&unlinked=true");
  assert.equal(taskLibraryURL({ projectId: "project & name", page: 2, limit: 10, unlinked: true }), "/api/v1/users/task-library?page=2&limit=10&project_id=project+%26+name");
});

test("project assistant wins when metadata arrives after config while a manual override remains stable",()=>{
 const profiles=[{id:"team",name:"Team",is_default:true},{id:"project",name:"Project"}];
 assert.equal(chooseAssistantID([],"","project"),"");
 assert.equal(chooseAssistantID(profiles,"","project"),"project");
 assert.equal(chooseAssistantID(profiles,"team","project",true),"project");
 assert.equal(chooseAssistantID(profiles,"team","project",false),"team");
 assert.equal(chooseAssistantID(profiles,"","disabled-project"),"");
});
