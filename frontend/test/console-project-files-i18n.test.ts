import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import cn from "../src/i18n/resources/cn.ts";
import en from "../src/i18n/resources/en.ts";

const sourceFiles = {
  files: readSource("../src/components/console/project/files.tsx"),
};
const combinedSource = Object.values(sourceFiles).join("\n");
const cjkPattern = /[\u3400-\u9fff]/;

function readSource(path: string) {
  return readFileSync(new URL(path, import.meta.url), "utf8");
}

test("项目文件树组件使用 consoleProject i18n key", () => {
  for (const source of Object.values(sourceFiles)) {
    assert.match(source, /useTranslation/);
  }

  assert.match(sourceFiles.files, /t\("consoleProject\.files\.title"\)/);
  assert.match(sourceFiles.files, /t\("consoleProject\.files\.empty"\)/);
  assert.doesNotMatch(combinedSource, cjkPattern);
});

test("项目文件树组件提供中英文资源", () => {
  assert.equal(cn.consoleProject.files.title, "项目文件");
  assert.equal(en.consoleProject.files.title, "Project files");
});
