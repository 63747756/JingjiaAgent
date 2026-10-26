import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import i18next from "i18next";
import { previewPortState } from "../src/utils/preview-port.ts";
import cn from "../src/i18n/resources/cn.ts";
import en from "../src/i18n/resources/en.ts";

test("a listener needs an explicit preview mapping before access", () => {
  assert.equal(previewPortState({ port: 8080, status: "reserved" }), "notOpen");
  assert.equal(previewPortState({ port: 8080, status: "connected", preview_url: "/preview", success: true }), "ready");
});

test("stopped services cannot be opened even when an old URL is present", () => {
  assert.equal(previewPortState({ forward_id: "test", status: "reserved", preview_url: "/preview", error_message: "Port is not listening" }), "notListening");
  assert.equal(previewPortState({ forward_id: "test", status: "reserved", success: false }), "notListening");
  assert.equal(previewPortState({ preview_url: "/preview", error_message: "gateway unavailable" }), "unavailable");
  assert.equal(previewPortState({ preview_url: "/preview", success: false }), "unavailable");
});

test("preview status labels are available in both languages", () => {
  for (const language of [cn, en]) {
    assert.ok(language.taskDetail.preview.notOpen);
    assert.ok(language.taskDetail.preview.notListening);
  }
});

test("every preview panel action and status resolves to translated text", async () => {
  const source = readFileSync(new URL("../src/components/console/task/task-preview-panel.tsx", import.meta.url), "utf8");
  const keys = new Set([...source.matchAll(/\bt\("([^"]+)"/g)].map(match => match[1]));
  for (const state of ["ready", "closed", "notOpen", "notListening", "unavailable"]) keys.add(`taskDetail.preview.${state}`);
  for (const [locale, resources] of Object.entries({ cn, en })) {
    const translator = i18next.createInstance();
    await translator.init({ lng: locale, fallbackLng: false, resources: { [locale]: { translation: resources } } });
    for (const key of keys) {
      assert.ok(translator.exists(key), `${locale}: missing preview translation ${key}`);
      assert.notEqual(translator.t(key), key, `${locale}: raw translation key shown in preview`);
    }
  }
});

// The backend returns closed even if the service is still running.
test("closing access is distinct from stopping the service", () => {
  assert.equal(previewPortState({ status: "closed" as never, forward_id: "test", success: false, error_message: "Preview access is closed" }), "closed");
});
