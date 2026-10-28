import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { EventEmitter } from "node:events";
import { PassThrough, Writable } from "node:stream";
import { afterEach, describe, expect, it, vi } from "vitest";
import { OpenCodeRunner } from "../src/runners/opencode.js";
import { writeStoredThread } from "../src/session-state.js";
import { runnerOptions, withTempSession } from "./helpers.js";

vi.mock("node:child_process", async (importOriginal) => ({
  ...await importOriginal<typeof import("node:child_process")>(),
  spawn: vi.fn(),
}));

afterEach(() => {
  vi.clearAllMocks();
  vi.unstubAllEnvs();
});

function fakeChild(write: (chunk: Buffer, done: (error?: Error | null) => void) => void) {
  const child = Object.assign(new EventEmitter(), {
    stdin: new Writable({ write(chunk, _encoding, done) { write(Buffer.from(chunk), done); } }),
    stdout: new PassThrough(),
    stderr: new PassThrough(),
    kill: vi.fn(() => true),
  });
  return {
    child,
    spawnResult: child as unknown as ChildProcessWithoutNullStreams,
    close(code = 0) {
      child.stdout.end();
      child.stderr.end();
      child.emit("exit", code, null);
    },
  };
}

function quietWriter() {
  return { write: vi.fn(), line: vi.fn(), transcript: () => "" };
}

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

describe("JingjiaAgent OpenCode prompt transport", () => {
  for (const nativeBridge of [true, false]) {
    it(`sends a complete long UTF-8 prompt through stdin for ${nativeBridge ? "the native bridge" : "the CLI fallback"}`, async () => {
      vi.stubEnv("AGENT_COMPOSE_RUN_ID", nativeBridge ? "run-long-input" : "");
      // Production execution overrides the image PATH without the office venv.
      vi.stubEnv("PATH", "/root/.local/bin:/usr/local/go/bin:/root/.cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin");
      await withTempSession(async (root) => {
        const prompt = "首行\n" + "长文本中文😀with spaces\n".repeat(12000) + "最后一行";
        const systemContext = "遵循系统规范，保留每一个字符。";
        const expectedInput = `${systemContext}\n\n${prompt}`;
        expect(Buffer.byteLength(expectedInput, "utf8")).toBeGreaterThan(128 * 1024);
        const chunks: Buffer[] = [];
        const process = fakeChild((chunk, done) => { chunks.push(chunk); done(); });
        process.child.stdin.once("finish", () => process.close());
        vi.mocked(spawn).mockReturnValueOnce(process.spawnResult);
        const options = { ...runnerOptions(root, systemContext, "opencode"), model: "provider/model-long-input" };
        await writeStoredThread(options.sessionRoot, "opencode", "ses_existing");
        const result = await new OpenCodeRunner(options, quietWriter()).runPrompt(prompt);

        expect(result.stopReason).toBe("completed");
        expect(Buffer.concat(chunks).toString("utf8")).toBe(expectedInput);
        expect(spawn).toHaveBeenCalledOnce();
        const [command, args, spawnOptions] = vi.mocked(spawn).mock.calls[0] as unknown as [string, string[], { stdio: string[] }];
        expect(command).toBe(nativeBridge ? "/opt/office/venv/bin/python" : "opencode");
        expect(args).toEqual([
          ...(nativeBridge ? ["/opt/agent-compose-runtime/jingjiaagent-opencode.py", "--prompt-stdin"] : []),
          "run", "--format", "json", "--dir", options.workspace,
          "--model", options.model, "--session", "ses_existing",
        ]);
        expect(JSON.stringify(args)).not.toContain(prompt);
        expect(JSON.stringify(args)).not.toContain(systemContext);
        expect(spawnOptions).toMatchObject({ stdio: ["pipe", "pipe", "pipe"] });
      });
    });
  }

  it("rejects a prematurely closed input pipe without exposing the prompt", async () => {
    vi.stubEnv("AGENT_COMPOSE_RUN_ID", "run-pipe-failure");
    await withTempSession(async (root) => {
      const prompt = "private-prompt-marker".repeat(9000);
      const pipeError = Object.assign(new Error(`broken pipe: ${prompt}`), { code: "EPIPE" });
      const process = fakeChild((_chunk, done) => done(pipeError));
      process.child.stdin.once("error", () => process.close());
      vi.mocked(spawn).mockReturnValueOnce(process.spawnResult);
      const writer = quietWriter();
      const runner = new OpenCodeRunner(runnerOptions(root, "", "opencode"), writer);

      await expect(runner.runPrompt(prompt)).rejects.toThrow(/^failed to send prompt to OpenCode$/);
      expect(writer.write).not.toHaveBeenCalled();
      expect(writer.line).not.toHaveBeenCalled();
    });
  });

  it("retains cancellation when cancelling also breaks the input pipe", async () => {
    vi.stubEnv("AGENT_COMPOSE_RUN_ID", "run-cancel-input");
    await withTempSession(async (root) => {
      const abortController = new AbortController();
      const process = fakeChild((_chunk, done) => {
        abortController.abort();
        done(Object.assign(new Error("broken pipe"), { code: "EPIPE" }));
      });
      process.child.stdin.once("error", () => {
        process.child.emit("error", Object.assign(new Error("aborted"), { name: "AbortError" }));
        process.close(143);
      });
      vi.mocked(spawn).mockReturnValueOnce(process.spawnResult);
      const runner = new OpenCodeRunner({ ...runnerOptions(root, "", "opencode"), abortController }, quietWriter());

      await expect(runner.runPrompt("取消前正在写入。".repeat(20000))).resolves.toMatchObject({ stopReason: "cancelled" });
    });
  });

  it("reports a failed process start without hanging or an unhandled pipe error", async () => {
    vi.stubEnv("AGENT_COMPOSE_RUN_ID", "run-missing-python");
    await withTempSession(async (root) => {
      const process = fakeChild((_chunk, done) => {
        setImmediate(() => {
          process.child.emit("error", Object.assign(new Error("spawn /opt/office/venv/bin/python ENOENT"), { code: "ENOENT" }));
          done(Object.assign(new Error("stream destroyed"), { code: "ERR_STREAM_DESTROYED" }));
          process.close(1);
        });
      });
      vi.mocked(spawn).mockReturnValueOnce(process.spawnResult);
      const runner = new OpenCodeRunner(runnerOptions(root, "", "opencode"), quietWriter());

      await expect(runner.runPrompt("waiting input".repeat(20000))).rejects.toThrow("spawn /opt/office/venv/bin/python ENOENT");
    });
  }, 2000);
});
