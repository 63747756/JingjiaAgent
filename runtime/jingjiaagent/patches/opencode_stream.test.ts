import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { EventEmitter } from "node:events";
import fs from "node:fs";
import fsPromises from "node:fs/promises";
import path from "node:path";
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
      child.emit("close", code, null);
    },
  };
}

function mockConfigCheck(config?: unknown, exitCode = 0) {
  let instructionsPath = "";
  vi.mocked(spawn).mockImplementationOnce((_command, args, options: any) => {
    expect(args).toEqual(["debug", "config", "--pure"]);
    instructionsPath = options.env.JINGJIAAGENT_SYSTEM_INSTRUCTIONS;
    const process = fakeChild((_chunk, done) => done());
    setImmediate(() => {
      process.child.stdout.write(typeof config === "string" ? config : JSON.stringify(config ?? {instructions: [instructionsPath]}));
      process.close(exitCode);
    });
    return process.spawnResult;
  });
  return () => instructionsPath;
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
        expect(Buffer.byteLength(prompt, "utf8")).toBeGreaterThan(128 * 1024);
        const chunks: Buffer[] = [];
        let instructionsPath = "";
        const process = fakeChild((chunk, done) => { chunks.push(chunk); done(); });
        process.child.stdin.once("finish", () => process.close());
        if (!nativeBridge) mockConfigCheck();
        vi.mocked(spawn).mockImplementationOnce((_command, _args, spawnOptions: any) => {
          instructionsPath = spawnOptions.env.JINGJIAAGENT_SYSTEM_INSTRUCTIONS;
          expect(fs.readFileSync(instructionsPath, "utf8")).toBe(systemContext);
          return process.spawnResult;
        });
        const options = { ...runnerOptions(root, systemContext, "opencode"), model: "provider/model-long-input" };
        await writeStoredThread(options.sessionRoot, "opencode", "ses_existing");
        const result = await new OpenCodeRunner(options, quietWriter()).runPrompt(prompt);

        expect(result.stopReason).toBe("completed");
        const input = Buffer.concat(chunks).toString("utf8");
        if (nativeBridge) {
          expect(JSON.parse(input)).toEqual({version: 1, prompt, runtime: {
            run_id: "run-long-input",
            instructions_path: instructionsPath, instructions_sha256: expect.stringMatching(/^[a-f0-9]{64}$/),
          }});
          expect(input).not.toContain(systemContext);
        } else expect(input).toBe(prompt);
        expect(fs.existsSync(path.dirname(instructionsPath))).toBe(false);
        expect(spawn).toHaveBeenCalledTimes(nativeBridge ? 1 : 2);
        const [command, args, spawnOptions] = vi.mocked(spawn).mock.calls.at(-1) as unknown as [string, string[], { stdio: string[] }];
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

  it("isolates per-Run system snapshots and leaves all user config sources untouched", async () => {
    await withTempSession(async (root) => {
      const original = '// JSONC stays native\n{"instructions":["{env:USER_RULE}"],"plugin":["./plugin.mjs"],"skills":{"paths":["/platform/selected-skills","/user/skills"]}}';
      const inline = '// inline JSONC\n{"permission":{"edit":"deny"}}';
      const base = path.join(root, "base.jsonc");
      fs.writeFileSync(base, original);
      vi.stubEnv("OPENCODE_CONFIG", base);
      vi.stubEnv("OPENCODE_CONFIG_CONTENT", inline);
      vi.stubEnv("OPENCODE_DISABLE_EXTERNAL_SKILLS", "true");
      const before = fs.readdirSync(root);
      const first = new OpenCodeRunner(runnerOptions(root, "FIRST_RUN_POLICY", "opencode"), quietWriter());
      const second = new OpenCodeRunner(runnerOptions(root, "SECOND_RUN_POLICY", "opencode"), quietWriter());
      try {
        const [a, b] = await Promise.all([first.environment(), second.environment()]);
        const firstPath = String(a.JINGJIAAGENT_SYSTEM_INSTRUCTIONS);
        const secondPath = String(b.JINGJIAAGENT_SYSTEM_INSTRUCTIONS);
        for (const env of [a, b]) {
          expect(env.OPENCODE_CONFIG).toBe(base);
          expect(env.OPENCODE_CONFIG_CONTENT).toBe(inline);
          expect(env.OPENCODE_DISABLE_EXTERNAL_SKILLS).toBe("true");
        }
        expect(firstPath).not.toBe(secondPath);
        expect(fs.statSync(firstPath).mode & 0o777).toBe(0o600);
        expect(fs.statSync(path.dirname(firstPath)).mode & 0o777).toBe(0o700);
        expect(fs.readFileSync(firstPath, "utf8")).toBe("FIRST_RUN_POLICY");
        expect(fs.readFileSync(secondPath, "utf8")).toBe("SECOND_RUN_POLICY");
        expect(fs.readdirSync(root)).toEqual(before);
        await first.cleanupSystemInstructions();
        expect(fs.existsSync(path.dirname(firstPath))).toBe(false);
        expect(fs.existsSync(secondPath)).toBe(true);
        expect(fs.readFileSync(base, "utf8")).toBe(original);
      } finally {
        await first.cleanupSystemInstructions();
        await second.cleanupSystemInstructions();
      }
    });
  });

  for (const config of [{instructions: ["project.md"]}, {instructions: [""]}, "malformed fixture-secret"]) {
    it(`blocks CLI user submission when native config verification fails: ${typeof config}`, async () => {
      vi.stubEnv("AGENT_COMPOSE_RUN_ID", "");
      await withTempSession(async (root) => {
        const policyPath = mockConfigCheck(config);
        const writer = quietWriter();
        const runner = new OpenCodeRunner(runnerOptions(root, "SYSTEM", "opencode"), writer);
        await expect(runner.runPrompt("PRIVATE_USER_MESSAGE")).rejects.toThrow(/^OpenCode system instructions (are not active|could not be verified)$/);
        expect(spawn).toHaveBeenCalledOnce();
        expect(fs.existsSync(path.dirname(policyPath()))).toBe(false);
        expect(writer.write).not.toHaveBeenCalled();
        expect(writer.line).not.toHaveBeenCalled();
      });
    });
  }

  it("does not expose native config output when CLI preflight exits unsuccessfully", async () => {
    vi.stubEnv("AGENT_COMPOSE_RUN_ID", "");
    await withTempSession(async (root) => {
      const policyPath = mockConfigCheck("fixture-secret", 1);
      const runner = new OpenCodeRunner(runnerOptions(root, "SYSTEM", "opencode"), quietWriter());
      await expect(runner.runPrompt("USER")).rejects.toThrow(/^OpenCode system instructions could not be verified$/);
      expect(spawn).toHaveBeenCalledOnce();
      expect(fs.existsSync(path.dirname(policyPath()))).toBe(false);
    });
  });

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

  it("cleans only its own temporary directory if writing the system snapshot fails", async () => {
    await withTempSession(async (root) => {
      const base = path.join(root, "base.jsonc");
      fs.writeFileSync(base, "// untouched\n{}");
      vi.stubEnv("OPENCODE_CONFIG", base);
      let instructionsPath = "";
      const write = vi.spyOn(fsPromises, "writeFile").mockImplementationOnce(async file => {
        instructionsPath = String(file);
        throw Object.assign(new Error("fixture write failure"), {code: "EIO"});
      });
      try {
        const runner = new OpenCodeRunner(runnerOptions(root, "POLICY", "opencode"), quietWriter());
        await expect(runner.environment()).rejects.toThrow("fixture write failure");
        expect(fs.existsSync(path.dirname(instructionsPath))).toBe(false);
        expect(fs.readFileSync(base, "utf8")).toBe("// untouched\n{}");
      } finally {
        write.mockRestore();
      }
    });
  });

  it("keeps instructions available until an errored native process has exited", async () => {
    vi.stubEnv("AGENT_COMPOSE_RUN_ID", "run-event-failure");
    await withTempSession(async (root) => {
      let instructionsPath = "";
      let readableBeforeExit = false;
      const process = fakeChild((_chunk, done) => done());
      process.child.stdin.once("finish", () => {
        process.child.stdout.end(JSON.stringify({type: "error", message: "fixture failure"}) + "\n");
      });
      process.child.kill.mockImplementation(() => {
        setTimeout(() => {
          readableBeforeExit = fs.existsSync(instructionsPath);
          process.close(1);
        }, 20);
        return true;
      });
      vi.mocked(spawn).mockImplementationOnce((_command, _args, options: any) => {
        instructionsPath = options.env.JINGJIAAGENT_SYSTEM_INSTRUCTIONS;
        return process.spawnResult;
      });
      const runner = new OpenCodeRunner(runnerOptions(root, "FROZEN_POLICY", "opencode"), quietWriter());
      await expect(runner.runPrompt("user request")).rejects.toThrow();
      expect(readableBeforeExit).toBe(true);
      expect(fs.existsSync(path.dirname(instructionsPath))).toBe(false);
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

  it("waits for a cancelled CLI preflight to exit before removing its system file", async () => {
    vi.stubEnv("AGENT_COMPOSE_RUN_ID", "");
    await withTempSession(async (root) => {
      const abortController = new AbortController();
      let instructionsPath = "";
      let readableBeforeExit = false;
      vi.mocked(spawn).mockImplementationOnce((_command, _args, options: any) => {
        instructionsPath = options.env.JINGJIAAGENT_SYSTEM_INSTRUCTIONS;
        const process = fakeChild((_chunk, done) => done());
        setImmediate(() => {
          abortController.abort();
          process.child.emit("error", Object.assign(new Error("aborted"), {name: "AbortError"}));
          setTimeout(() => {
            readableBeforeExit = fs.existsSync(instructionsPath);
            process.close(143);
          }, 10);
        });
        return process.spawnResult;
      });
      const runner = new OpenCodeRunner({...runnerOptions(root, "POLICY", "opencode"), abortController}, quietWriter());
      await expect(runner.runPrompt("USER")).resolves.toMatchObject({stopReason: "cancelled"});
      expect(readableBeforeExit).toBe(true);
      expect(fs.existsSync(path.dirname(instructionsPath))).toBe(false);
      expect(spawn).toHaveBeenCalledOnce();
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
