import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
const read = (path: string) => readFileSync(new URL(path, import.meta.url), "utf8");
test("自有导航不继承上游活动、收费、社区和升级入口", () => {
 const sidebar = read("../src/components/console/nav/user-sidebar.tsx");
 const shell = read("../src/components/welcome/terminal-chrome.tsx");
 assert.match(sidebar, /NavProject/); assert.match(sidebar, /NavBalance/); assert.match(sidebar, /settings/);
 assert.match(shell, /TerminalHeader/); assert.match(shell, /TerminalFooter/);
 assert.doesNotMatch(sidebar + shell, /baizhi\.cloud|monkeycode-ai|discord\.gg|NavInvite|NavEssay|NavCheckin|NavCommunity|CONSULT_PURCHASE|GITHUB_LINK/);
 assert.match(sidebar + shell, /BRAND\.chineseName/); assert.match(sidebar + shell, /PRODUCT_REVISION/);
});
