import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import i18next from "i18next";
import { applyPreviewDiscovery, openPreviewWindow, previewPortState, startPreviewPolling, type TaskPreviewPort } from "../src/utils/preview-port.ts";
import cn from "../src/i18n/resources/cn.ts";
import en from "../src/i18n/resources/en.ts";

test("running listeners without routes remain available for first access", () => {
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
  for (const state of ["ready", "closed", "notOpen", "notListening", "unavailable", "unknown"]) keys.add(`taskDetail.preview.${state}`);
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

test("failed discovery keeps known services uncertain, but confirmed stopped states clear them", () => {
  const known: TaskPreviewPort[] = [{ port: 8000, status: "connected", preview_url: "https://route.preview.example.com", discovery_state: "ready" }];
  const unknown = applyPreviewDiscovery(known, [], "unknown", false);
  assert.equal(unknown?.length, 1);
  assert.equal(previewPortState(unknown![0]), "unknown");
  assert.deepEqual(applyPreviewDiscovery(known, [], "stopped", false), []);
  assert.deepEqual(applyPreviewDiscovery(known, [], "unconfigured", false), []);
  assert.deepEqual(applyPreviewDiscovery(known, [], "ready", true), []);
  assert.deepEqual(applyPreviewDiscovery(known, [], "unknown", true), []);
  assert.equal(known[0].discovery_state, "ready");
});

test("a complete scan removes stopped listeners while retaining uncertain HTTP listeners", () => {
  const updated: TaskPreviewPort[] = [{ port: 8080, discovery_state: "unknown" }, { port: 9000, discovery_state: "ready" }];
  assert.deepEqual(applyPreviewDiscovery([{ port: 8000 }, { port: 8080 }], updated, "unknown", true), updated);
});

test("after discovery cache loss, uncertain listening ports preserve known HTTP rows without inventing new services", () => {
  const previous: TaskPreviewPort[] = [{ port: 8000, status: "connected", discovery_state: "ready" }, { port: 8080, status: "closed", discovery_state: "ready" }, { port: 9000, discovery_state: "ready" }];
  const result = applyPreviewDiscovery(previous, [], "unknown", true, [8000, 8080, 6379]);
  assert.deepEqual(result?.map(port => port.port), [8000, 8080]);
  assert.ok(result?.every(port => port.discovery_state === "unknown"));
  assert.equal(result?.[1].status, "closed");
  assert.equal(result?.some(port => port.port === 6379), false);
  assert.equal(result?.some(port => port.port === 9000), false);
  const confirmed = { port: 8000, status: "closed", discovery_state: "ready" as const };
  assert.deepEqual(applyPreviewDiscovery(previous, [confirmed], "unknown", true, [8000]), [confirmed]);
});

test("preview polling stops in background tabs and on panel disposal", t => {
  t.mock.timers.enable({ apis: ["setInterval"] });
  let count = 0;
  let change: (() => void) | undefined;
  const page = {
    visibilityState: "visible",
    addEventListener: (_: string, listener: () => void) => { change = listener; },
    removeEventListener: (_: string, listener: () => void) => { assert.equal(change, listener); change = undefined; },
  };
  const stop = startPreviewPolling(() => { count++; }, page as unknown as Document);
  assert.equal(count, 1);
  t.mock.timers.tick(20000);
  assert.equal(count, 3);
  page.visibilityState = "hidden"; change!();
  t.mock.timers.tick(60000);
  assert.equal(count, 3);
  page.visibilityState = "visible"; change!();
  assert.equal(count, 4);
  t.mock.timers.tick(10000);
  assert.equal(count, 5);
  stop(); t.mock.timers.tick(30000);
  assert.equal(count, 5);
  assert.equal(change, undefined);
});

test("opening a panel in a hidden tab does not discover services", t => {
  t.mock.timers.enable({ apis: ["setInterval"] });
  let count = 0;
  const page = { visibilityState: "hidden", addEventListener() {}, removeEventListener() {} };
  const stop = startPreviewPolling(() => { count++; }, page as unknown as Document);
  t.mock.timers.tick(60000); stop();
  assert.equal(count, 0);
});

test("visit reserves a window synchronously, detaches its opener and navigates only after route creation", async () => {
  const events: string[] = [];
  let resolve!: (url: string) => void;
  const popup = { opener: "console", closed: false, close() { events.push("close"); }, location: { replace(url: string) { events.push(url); } } };
  const opened = openPreviewWindow(() => { events.push("window"); return popup as unknown as Window; }, () => {
    assert.equal(popup.opener, null);
    events.push("request");
    return new Promise<string>(r => { resolve = r; });
  }, { blocked: "blocked", unavailable: "unavailable" });
  assert.deepEqual(events, ["window", "request"]);
  resolve("https://route.preview.example.com/app");
  await opened;
  assert.deepEqual(events, ["window", "request", "https://route.preview.example.com/app"]);
});

test("failed, unsafe or blocked preview access leaves no blank window and cannot publish silently", async () => {
  let requested = false;
  await assert.rejects(openPreviewWindow(() => null, async () => { requested = true; return "https://preview.example.com"; }, { blocked: "blocked", unavailable: "unavailable" }), /blocked/);
  assert.equal(requested, false);
  for (const request of [async () => { throw new Error("service stopped"); }, async () => "javascript:alert(1)"]) {
    let closed = false;
    const popup = { opener: null, closed: false, close() { closed = true; }, location: { replace() { assert.fail("must not navigate"); } } };
    await assert.rejects(openPreviewWindow(() => popup as unknown as Window, request, { blocked: "blocked", unavailable: "unavailable" }));
    assert.equal(closed, true);
  }
});
