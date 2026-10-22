import assert from "node:assert/strict";
import test from "node:test";
import { workPreviewKind, workPreviewImage, workPreviewLink, workPreviewBlob } from "../src/lib/work-previews.ts";

test("preview routing uses supported response MIME types rather than executable filename claims", () => {
  assert.equal(workPreviewKind("TEXT/HTML; charset=utf-8"), "html");
  assert.equal(workPreviewKind("text/markdown; charset=utf-8"), "markdown");
  assert.equal(workPreviewKind("text/plain"), "text");
  for (const kind of ["png", "jpeg", "gif", "webp", "bmp", "svg+xml"]) assert.equal(workPreviewKind("image/" + kind), "image");
  assert.equal(workPreviewKind("application/pdf"), "pdf");
  assert.equal(workPreviewKind("application/javascript"), null);
  assert.equal(workPreviewKind("application/octet-stream"), null);
});

test("Markdown does not automatically fetch application or remote images and refuses active URLs", () => {
  for (const url of ["/api/v1/users/work-tasks", "https://remote.invalid/a.png", "data:image/svg+xml;base64,PHN2Zz4=", "javascript:alert(1)"]) assert.equal(workPreviewImage(url), "");
  assert.equal(workPreviewImage("data:image/png;base64,iVBORw0KGgo="), "data:image/png;base64,iVBORw0KGgo=");
  assert.equal(workPreviewLink("https://example.com/report"), "https://example.com/report");
  for (const url of ["/api/v1/users/work-tasks", "javascript:alert(1)", "data:text/html,a", "https:\n//example.com"]) assert.equal(workPreviewLink(url), "");
});

test("preview body limit is enforced even without Content-Length", async () => {
  const good = await workPreviewBlob(new Response("中文", { headers: { "Content-Type": "text/plain; charset=utf-8" } }), 8);
  assert.equal(await good.text(), "中文");
  await assert.rejects(workPreviewBlob(new Response("12345"), 4), RangeError);
  await assert.rejects(workPreviewBlob(new Response("x", { headers: { "Content-Length": "500" } }), 4), RangeError);
  await assert.rejects(workPreviewBlob(new Response(""), 4), /preview_empty/);
});
