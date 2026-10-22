import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const source = readFileSync(
  new URL("../src/components/console/task/chat-inputbox.tsx", import.meta.url),
  "utf8",
);

test("任务对话输入栏在窄屏隔离输入区和操作区", () => {
  const composerSource = readFileSync(new URL("../src/components/console/message-composer.tsx", import.meta.url), "utf8");
  assert.match(source, /<MessageComposerTextarea/);
  assert.match(
    composerSource,
    /min-w-0 w-full resize-none overflow-y-auto overscroll-contain/,
  );
  assert.match(
    source,
    /className="flex w-full min-w-0 flex-row items-center justify-between gap-2"/,
  );
  assert.match(
    source,
    /className="no-scrollbar flex min-w-0 flex-1 flex-row items-center gap-2 overflow-x-auto"/,
  );
  assert.match(
    source,
    /className="flex min-w-0 shrink flex-row items-center justify-end gap-1.5"/,
  );
});

test("手机端操作保持 44px 触控区和发送可访问名称", () => {
  const mentionSource = readFileSync(new URL("../src/components/console/work/skill-mention-picker.tsx", import.meta.url), "utf8");
  const mobileTouchTargets = (source + mentionSource).match(/max-sm:size-11/g) ?? [];
  assert.ok(mobileTouchTargets.length >= 5);
  assert.match(source, /aria-label=\{t\("taskDetail\.common\.send"\)\}/);
  assert.match(
    source,
    /<ArrowUp className="size-4" \/>/,
  );
  assert.match(
    source,
    /<VoiceInputButton[\s\S]*?className="max-sm:min-h-11 max-sm:min-w-11"/,
  );
});
