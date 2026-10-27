import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import cn from "../src/i18n/resources/cn.ts";
import en from "../src/i18n/resources/en.ts";

const sourceFiles = {
  taskActions: readSource("../src/components/console/task/task-actions-dropdown.tsx"),
  modelSelect: readSource("../src/components/console/task/model-select.tsx"),
  skillSelector: readSource("../src/components/console/task/task-skill-selector.tsx"),
  pluginSelector: readSource("../src/components/console/task/task-plugin-selector.tsx"),
  voiceInput: readSource("../src/components/console/task/voice-input-button.tsx"),
};

function readSource(path: string) {
  return readFileSync(new URL(path, import.meta.url), "utf8");
}

test("任务工作流共享组件使用 taskWorkflow i18n key", () => {
  assert.match(sourceFiles.taskActions, /t\("taskWorkflow\.actions\.rename"\)/);
  assert.match(sourceFiles.taskActions, /t\("taskWorkflow\.actions\.stop"\)/);
  assert.match(sourceFiles.taskActions, /t\("taskWorkflow\.actions\.delete"\)/);
  assert.match(sourceFiles.taskActions, /t\("taskWorkflow\.rename\.title"\)/);

  assert.match(sourceFiles.modelSelect, /t\("taskWorkflow\.model\.basic"\)/);
  assert.match(sourceFiles.modelSelect, /t\("taskWorkflow\.model\.recommended"\)/);

  assert.match(sourceFiles.skillSelector, /t\("taskWorkflow\.skill\.label"\)/);
  assert.match(sourceFiles.pluginSelector, /t\("taskWorkflow\.plugin\.empty"\)/);
  assert.match(sourceFiles.voiceInput, /t\("taskWorkflow\.voice\.recording"\)/);
});

test("任务工作流共享组件提供中英文资源", () => {
  assert.equal(cn.taskWorkflow.model.basic, "基础模型");
  assert.equal(en.taskWorkflow.model.basic, "Basic model");
  assert.equal(cn.taskWorkflow.voice.recording, "正在录音");
  assert.equal(en.taskWorkflow.voice.recording, "Recording");
  assert.equal(cn.taskWorkflow.actions.stop, "终止任务");
  assert.equal(en.taskWorkflow.actions.stop, "Stop");
  assert.equal(cn.taskWorkflow.actions.delete, "删除任务");
  assert.equal(en.taskWorkflow.actions.delete, "Delete");
});

test("模型选择框推荐标按同级别模型 weight 计算", () => {
  assert.match(sourceFiles.modelSelect, /recommendedModelKeys/);
  assert.match(sourceFiles.modelSelect, /getBuiltinModelName\(model\.model\) === option\.model/);
  assert.match(sourceFiles.modelSelect, /\(right\.weight \|\| 0\) - \(left\.weight \|\| 0\)/);
  assert.doesNotMatch(sourceFiles.modelSelect, /qwen3\.5-plus|qwen3\.6-plus|gpt-5\.5/);
});
