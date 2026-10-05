import { describe, expect, it } from "vitest";
import { OpenCodeRunner } from "../src/runners/opencode.js";
import { runnerOptions } from "./helpers.js";

describe("JingjiaAgent native text streaming", () => {
  it("publishes deltas once and preserves the final full result without transcript duplication", () => {
    const events: any[] = [];
    let transcript = "";
    const runner = new OpenCodeRunner({ ...runnerOptions("/tmp/stream", "", "opencode"), onEvent: event => events.push(event) },
      { write: text => { transcript += text; }, line: text => { transcript += text + "\n"; } });
    const result: any = {};
    runner.handleEvent({ type: "step_start" }, result);
    for (const text of ["中文", "回答"]) runner.handleEvent({ type: "jingjiaagent_text_delta", sessionID: "ses_1", part: { text, messageID: "msg_1" } }, result);
    runner.handleEvent({ type: "text", jingjiaagent_streamed: true, part: { text: "中文回答", messageID: "msg_1" } }, result);
    runner.handleEvent({ type: "step_finish", part: { reason: "stop" } }, result);
    expect(events.filter(event => event.kind === "text_delta").map(event => event.text)).toEqual(["中文", "回答"]);
    expect(transcript).toBe("中文回答");
    expect(result.finalText).toBe("中文回答");
    expect(result.threadId).toBe("ses_1");
  });
});
