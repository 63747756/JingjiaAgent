import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import cn from "../src/i18n/resources/cn.ts";
import en from "../src/i18n/resources/en.ts";

type MetaResources = {
  meta?: {
    default: {
      title: string;
      description: string;
      keywords: string;
    };
  };
};

type DocumentMetaModule = {
  patchDocumentMeta: (
    translate: (key: string) => string,
    targetDocument: Document,
  ) => void;
};

type FakeMetaElement = {
  name: string;
  content: string;
  setAttribute: (name: string, value: string) => void;
};

async function loadDocumentMetaModule(): Promise<DocumentMetaModule> {
  try {
    return await import("../src/i18n/document-meta.ts");
  } catch (error) {
    assert.fail(
      `document meta module should exist: ${error instanceof Error ? error.message : String(error)}`,
    );
  }
}

function createMetaElement(name = "", content = ""): FakeMetaElement {
  return {
    name,
    content,
    setAttribute(attributeName, value) {
      if (attributeName === "name") {
        this.name = value;
      }

      if (attributeName === "content") {
        this.content = value;
      }
    },
  };
}

function createFakeDocument() {
  const metas = new Map<string, FakeMetaElement>();
  metas.set("description", createMetaElement("description", "default description"));
  metas.set("keywords", createMetaElement("keywords", "default keywords"));

  const fakeDocument = {
    title: "Default title",
    head: {
      appendChild(element: FakeMetaElement) {
        metas.set(element.name, element);
        return element;
      },
    },
    createElement(tagName: string) {
      assert.equal(tagName, "meta");
      return createMetaElement();
    },
    querySelector(selector: string) {
      const match = /^meta\[name="([^"]+)"\]$/.exec(selector);
      return match ? metas.get(match[1]) ?? null : null;
    },
  };

  return {
    document: fakeDocument as unknown as Document,
    metas,
  };
}

test("全局 meta 翻译资源提供中英文默认文案", () => {
  assert.deepEqual((cn as MetaResources).meta?.default, {
    title: "景嘉微AI助手 · JingjiaAgent",
    description:
      "景嘉微AI助手 是面向日常工作的远程 AI 平台，支持任务、业务项目、版本资料、Skills 和成果管理，提供数据分析、文档报告、演示文稿与代码开发入口。",
    keywords: "远程Agent, AI工作平台, 数据分析, 文档报告, 演示文稿, 业务项目, Skills, 景嘉微AI助手",
  });

  assert.deepEqual((en as MetaResources).meta?.default, {
    title: "JingjiaAgent",
    description:
      "JingjiaAgent is a remote AI work platform with tasks, business projects, versioned resources, Skills, and deliverables for data analysis, reports, presentations, and code development.",
    keywords: "remote Agent, AI workspace, data analysis, reports, presentations, business projects, Skills, JingjiaAgent",
  });
});

test("patchDocumentMeta 根据当前语言翻译更新全局 title 和 meta", async () => {
  const { patchDocumentMeta } = await loadDocumentMetaModule();
  const { document, metas } = createFakeDocument();
  const translations: Record<string, string> = {
    "meta.default.title": "JingjiaAgent Intelligent Development Platform",
    "meta.default.description": "Sign in to JingjiaAgent",
    "meta.default.keywords": "AI code generation, JingjiaAgent",
  };

  patchDocumentMeta((key) => translations[key] ?? key, document);

  assert.equal(document.title, translations["meta.default.title"]);
  assert.equal(metas.get("description")?.content, translations["meta.default.description"]);
  assert.equal(metas.get("keywords")?.content, translations["meta.default.keywords"]);
});

test("initI18n 负责触发全局 document meta patch", () => {
  const i18nSource = readFileSync(
    new URL("../src/i18n/index.ts", import.meta.url),
    "utf8",
  );

  assert.match(i18nSource, /patchDocumentMeta/);
  assert.match(i18nSource, /i18n\.t/);
});
