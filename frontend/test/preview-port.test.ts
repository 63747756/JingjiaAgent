import assert from "node:assert/strict";
import test from "node:test";
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
