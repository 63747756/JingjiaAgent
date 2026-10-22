import assert from "node:assert/strict";
import test from "node:test";
import { TaskMessageHandler } from "../src/components/console/task/task-message-handler.ts";
import { b64encode } from "../src/utils/message-data.ts";

test("a rejected skill reference exposes the server reason and marks delivery failed", () => {
  const handler = new TaskMessageHandler();
  const reason = "引用技能已停用、删除或不再允许引用，请移除引用";
  handler.applyOptimisticUserInput({ content: "保留草稿", client_message_id: "rejected-input", attachments: [] });
  const state = handler.pushChunk({ event: "error", data: b64encode(JSON.stringify(reason)), timestamp: Date.now() });
  assert.equal(state.status, "error");
  assert.equal(state.messages[0].data.deliveryState, "failed");
  assert.equal(state.messages.at(-1)?.data.details, reason);
});

test("structured runtime errors keep details and malformed errors have readable fallback", () => {
  for (const payload of [{ details: "原生执行器错误", message: "摘要" }, { message: "后台校验失败" }, null, {}]) {
    const handler = new TaskMessageHandler();
    const state = handler.pushChunk({ event: "task-error", data: b64encode(JSON.stringify(payload)), timestamp: Date.now() });
    const detail = state.messages.at(-1)?.data.details;
    assert.ok(typeof detail === "string" && detail.trim().length > 0);
    if (payload?.details) assert.equal(detail, payload.details);
    else if (payload?.message) assert.equal(detail, payload.message);
  }
});
