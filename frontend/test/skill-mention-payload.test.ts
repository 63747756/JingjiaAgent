import assert from "node:assert/strict";
import test from "node:test";
import { normalizeTaskUserInput, parseTaskUserInputPayload } from "../src/components/console/task/task-shared.ts";
import { TaskStreamClient } from "../src/components/console/task/task-stream-client.ts";

test("skill-only inputs retain the explicit ID and structured history label", () => {
  const skill = { id: "personal-skill", name: "生成html文件", version_id: "version-1", version: "1.0" };
  const input = normalizeTaskUserInput({ content: "", attachments: [], mentioned_skill_id: skill.id, skill_mention: skill });
  assert.equal(input.content, "");
  assert.equal(input.mentioned_skill_id, skill.id);
  const history = parseTaskUserInputPayload(JSON.stringify({ ...input, content: Buffer.from(input.content).toString("base64") }));
  assert.deepEqual(history, input);
  const oldHistory = parseTaskUserInputPayload(JSON.stringify({ content: "", attachments: null, skill_mention: skill }));
  assert.deepEqual(oldHistory.attachments, []);
  assert.deepEqual(oldHistory.skill_mention, skill);
});

test("socket connection and optimistic labels cannot acknowledge a submitted input", () => {
  const id = "d874a86c-ffcf-4b6b-a8a6-4b64d3749701";
  const client = TaskStreamClient.new({ taskId: "task", userInput: {client_message_id:id, content:"", attachments:[],mentioned_skill_id:"skill"} });
  const state = client.getState();
  assert.equal(client.hasAcceptedInput(state), false);
  const message = { id:"input",time:1,role:"user" as const,type:"user_input" as const,data:{clientMessageId:id,deliveryState:"sending" as const} };
  assert.equal(client.hasAcceptedInput({...state,messages:[message]}), false);
  assert.equal(client.hasAcceptedInput({...state,messages:[{...message,data:{clientMessageId:id,deliveryState:"confirmed"}}]}), true);
  assert.equal(client.hasAcceptedInput({...state,messages:[{...message,data:{clientMessageId:"other",deliveryState:"confirmed"}}]}), false);
});

test("removing a message reference leaves plain text and attachments intact", () => {
  const input = normalizeTaskUserInput({ content: "分析这份资料", attachments: [{ url: "https://example.test/table.csv", filename: "table.csv" }] });
  assert.equal(input.mentioned_skill_id, undefined);
  assert.equal(input.skill_mention, undefined);
  assert.equal(input.attachments.length, 1);
  const parsed = parseTaskUserInputPayload(JSON.stringify({ ...input, content: Buffer.from(input.content).toString("base64") }));
  assert.deepEqual(parsed, input);
});
