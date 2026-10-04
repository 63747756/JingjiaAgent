import assert from "node:assert/strict";
import test from "node:test";

import { formatExtensionImportResult } from "../src/pages/console/manager/extension-package.ts";

const translate = (key: string, options?: Record<string, unknown>) => {
  assert.equal(key, "managerSkills.extensionImport.summary");
  return `新增 ${options?.createdRules} 条规则，更新 ${options?.updatedRules} 条规则，新增 ${options?.createdSkills} 个 Skills，更新 ${options?.updatedSkills} 个 Skills，新增 ${options?.createdImages} 个镜像，更新 ${options?.updatedImages} 个镜像`;
};

test("格式化扩展包导入结果", () => {
  assert.equal(
    formatExtensionImportResult({
      created_rules: 5,
      updated_rules: 6,
      created_skills: 1,
      updated_skills: 2,
      created_images: 3,
      updated_images: 4,
    }, translate),
    "新增 5 条规则，更新 6 条规则，新增 1 个 Skills，更新 2 个 Skills，新增 3 个镜像，更新 4 个镜像",
  );
});

test("格式化扩展包导入结果时缺省计数按 0 处理", () => {
  assert.equal(
    formatExtensionImportResult({}, translate),
    "新增 0 条规则，更新 0 条规则，新增 0 个 Skills，更新 0 个 Skills，新增 0 个镜像，更新 0 个镜像",
  );
});
