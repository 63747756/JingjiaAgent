import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

function readSource(path) {
  return readFileSync(new URL(path, import.meta.url), "utf8").replaceAll("\r\n", "\n");
}

const selector = readSource(
  "../src/components/console/task/task-skill-selector.tsx",
);
function loadWheelScrollDelta() {
  const match = selector.match(
    /function getWheelScrollDelta\([^)]*\) \{([\s\S]*?)\n\}\n\ntype DomainSkill/,
  );

  assert.ok(match, "未找到滚轮增量换算函数");

  const body = match[1]
    .replaceAll("DOM_DELTA_LINE", "1")
    .replaceAll("DOM_DELTA_PAGE", "2")
    .replaceAll("WHEEL_LINE_HEIGHT", "16");

  return new Function("deltaY", "deltaMode", "pageHeight", body);
}

test("技能选择器支持按需启用滚轮回退", () => {
  assert.match(selector, /enableWheelScrollFallback\?: boolean/);
  assert.match(selector, /addEventListener\("wheel", handleSkillListWheel, \{ passive: false \}\)/);
  assert.match(selector, /event\.preventDefault\(\)/);
  assert.match(selector, /removeEventListener\("wheel", handleSkillListWheel\)/);
  assert.match(
    selector,
    /\[enableWheelScrollFallback, open, popoverContentElement\]/,
  );
  assert.match(selector, /setPopoverContentElement/);
  assert.match(selector, /activeSkillList\.scrollTop \+= getWheelScrollDelta/);
  assert.match(selector, /closest<HTMLDivElement>\(ACTIVE_SKILL_LIST_SELECTOR\)/);
  assert.match(selector, /ref=\{setPopoverContentElement\}/);
  assert.match(selector, /event\.ctrlKey/);
  assert.match(
    selector,
    /Math\.abs\(event\.deltaY\) <= Math\.abs\(event\.deltaX\)/,
  );
  assert.doesNotMatch(
    selector,
    /ref=\{tag === activeSkillTag \? setSkillListElement : undefined\}/,
  );
  assert.doesNotMatch(selector, /<Popover modal=/);
});

test("滚轮回退按 deltaMode 换算滚动距离", () => {
  const getWheelScrollDelta = loadWheelScrollDelta();

  assert.equal(getWheelScrollDelta(24, 0, 300), 24);
  assert.equal(getWheelScrollDelta(3, 1, 300), 48);
  assert.equal(getWheelScrollDelta(2, 2, 300), 600);
  assert.equal(getWheelScrollDelta(-2, 1, 300), -32);
});
