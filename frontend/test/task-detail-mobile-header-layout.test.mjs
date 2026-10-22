import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const read = path => readFileSync(new URL(path, import.meta.url), "utf8");
const source = read("../src/pages/console/user/task/task-detail.tsx");
const chat = read("../src/components/console/task/chat-inputbox.tsx");
const composer = read("../src/components/console/work/work-composer.tsx");
const selector = read("../src/components/console/task/model-select.tsx");
const shell = read("../src/pages/console/user/page.tsx");

test("任务名称与成果、高级工具处在同一可收缩页头", () => {
  assert.match(source, /getTaskDisplayName\(task,/);
  assert.match(source, /className="flex min-w-0 shrink-0 items-center justify-between gap-2"/);
  assert.match(source, /<h1 className="min-w-0 flex-1 truncate/);
  assert.doesNotMatch(source, /showAdvancedTools|setShowAdvancedTools/);
});

test("成果面板初始关闭并使用受控高亮", () => {
  assert.match(source, /useState<SidePanelType \| null>\(null\)/);
  assert.doesNotMatch(source, /setActiveSidePanel\("artifacts"\)/);
  assert.match(source, /aria-expanded=\{isMobile \? workArtifactsMobile : activeSidePanel === "artifacts"\}/);
  assert.match(source, /workArtifactsMobile : activeSidePanel === "artifacts"\) && "bg-accent text-primary"/);
  assert.match(source, /mobileToolsOpen && "bg-accent text-primary"/);
});

test("新任务和续聊共用输入区模型选择且包含搜索", () => {
  assert.match(source, /renderModelSelector=\{\(\) => <ModelSelect compact/);
  assert.match(chat, /renderModelSelector\?\.\(!canUseIdleControls \|\| uploadDialogOpen \|\| resolvingFile\)/);
  assert.match(composer, /<ModelSelect compact/);
  assert.match(selector, /side=\{compact \? "top" : undefined\}/);
  assert.match(selector, /taskWorkflow\.model\.search/);
});

test("工具菜单保留文件、预览、终端及上下文操作", () => {
  for (const key of ["files", "terminal", "preview"]) assert.match(source, new RegExp('taskDetail\\.panels\\.' + key));
  for (const action of ["handleCompactContext", "setResetContextDialogOpen", "runMobileToolAction"]) assert.ok(source.includes(action));
  assert.match(source, /<Popover modal open=\{mobileToolsOpen\}/);
  assert.doesNotMatch(source, /setSkillsDialogOpen|<TaskSkillsUpdateDialog/);
});

test("手机文件在工具菜单内展开，桌面使用侧栏", () => {
  assert.match(source, /isMobile \? setMobileToolsView\("files"\) : runMobileToolAction/);
  assert.match(source, /onClosePanel=\{\(\) => handleMobileToolsOpenChange\(false\)\}/);
  assert.match(source, /const hasSidePanel = !isMobile && activeSidePanel !== null/);
});

test("菜单对话动作在关闭后执行，避免焦点冲突", () => {
  assert.match(source, /pendingMobileToolActionRef\.current = action/);
  assert.match(source, /onCloseAutoFocus=\{handleMobileToolsCloseAutoFocus\}/);
  assert.match(source, /event\.preventDefault\(\)[\s\S]*?pendingMobileToolActionRef\.current = null[\s\S]*?action\(\)/);
});

test("导航时取消模型候选请求，不自动恢复成果侧栏", () => {
  assert.match(source, /model-options\?task_id=/);
  assert.match(source, /return \(\) => controller\.abort\(\)/);
  assert.match(source, /if \(!controller\.signal\.aborted\) setWorkModelOptions/);
  assert.match(composer, /if \(!controller\.signal\.aborted\) \{ setModelOptions/);
});

test("任务内容区域保持原来的独立滚动和手机外壳", () => {
  assert.match(shell, /<header className="flex h-15 shrink-0 items-center gap-2 overflow-hidden">/);
  assert.match(source, /<TaskUserInputIndex/);
  assert.doesNotMatch(source, /createPortal\(|fixed right-4 top-/);
  assert.match(source, /max-h-\[65dvh\] overflow-y-auto/);
});
