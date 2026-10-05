import { mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { nativeModelEnvironment, refreshNativeClaudeSettings } from "../src/jingjiaagent-sdk.js";

const task = "11111111-1111-1111-1111-111111111111";
const sandbox = {
  AGENT_COMPOSE_RUN_ID: "run-first",
  JINGJIAAGENT_TASK_ID: task,
  JINGJIAAGENT_MODEL_API_KEY: "sandbox-old-key",
  JINGJIAAGENT_MODEL_BASE_URL: "https://old.example/v1",
  JINGJIAAGENT_MODEL_NAME: "sandbox-old-model",
  ANTHROPIC_API_KEY: "facade-key",
  ANTHROPIC_AUTH_TOKEN: "old-token",
  ANTHROPIC_BASE_URL: "https://facade.example",
  OPENAI_API_KEY: "facade-key",
  OPENAI_BASE_URL: "https://facade.example",
};

describe("native per-command model configuration", () => {
  let root: string;
  let path: string;
  const config = (model = "current-model") => ({
    task_id: task, api_key: "current-key", base_url: "https://current.example/v1", model,
  });
  const save = (value: unknown) => writeFileSync(path, JSON.stringify(value), { mode: 0o600 });

  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "native-model-"));
    path = join(root, task + ".model.json");
  });
  afterEach(() => rmSync(root, { recursive: true, force: true }));

  it.each(["claude", "codex"])("uses the latest intent after a %s restart in the same Sandbox", provider => {
    save(config());
    const first = nativeModelEnvironment(provider, sandbox, root);
    const changed = { ...config("replacement-model"), api_key: "replacement-key", base_url: "https://replacement.example/v1" };
    save(changed);
    const second = nativeModelEnvironment(provider, { ...sandbox, AGENT_COMPOSE_RUN_ID: "run-second" }, root);
    const prefix = provider === "claude" ? "ANTHROPIC" : "OPENAI";
    expect(first[prefix + "_API_KEY"]).toBe("current-key");
    expect(second[prefix + "_API_KEY"]).toBe(changed.api_key);
    expect(second[prefix + "_BASE_URL"]).toBe(changed.base_url);
    expect(second[prefix + "_MODEL"]).toBe(changed.model);
    if (provider === "claude") expect(second.ANTHROPIC_AUTH_TOKEN).toBe(changed.api_key);
    expect(second.JINGJIAAGENT_MODEL_API_KEY).toBeUndefined();
    expect(second.JINGJIAAGENT_MODEL_BASE_URL).toBeUndefined();
    expect(second.JINGJIAAGENT_MODEL_NAME).toBeUndefined();
    expect(sandbox.JINGJIAAGENT_MODEL_API_KEY).toBe("sandbox-old-key");
    expect(sandbox.ANTHROPIC_AUTH_TOKEN).toBe("old-token");
  });

  it("does not require legacy Sandbox model variables", () => {
    save(config());
    const env = nativeModelEnvironment("claude", { AGENT_COMPOSE_RUN_ID: "run", JINGJIAAGENT_TASK_ID: task }, root);
    expect(env.ANTHROPIC_API_KEY).toBe("current-key");
    expect(env.ANTHROPIC_BASE_URL).toBe("https://current.example/v1");
  });

  it("refreshes overriding Claude settings after LLM-only restart without losing permissions or plugins", () => {
    mkdirSync(join(root,".claude"));
    const settingsPath=join(root,".claude","settings.json");
    const retained={permissions:{deny:["Bash(rm:*)"]},enabledPlugins:{"private-tool":true},
      env:{ANTHROPIC_AUTH_TOKEN:"old-token",ANTHROPIC_BASE_URL:"https://old.example",ANTHROPIC_DEFAULT_HAIKU_MODEL:"old-model",CUSTOM_ENV:"retained"}};
    writeFileSync(settingsPath,JSON.stringify(retained));
    for(const selected of [config(),{...config("next-model"),api_key:"next-key",base_url:"https://next.example"}]) {
      save(selected);
      refreshNativeClaudeSettings(nativeModelEnvironment("claude",sandbox,root),root);
      const actual=JSON.parse(readFileSync(settingsPath,"utf8"));
      expect(actual.permissions).toEqual(retained.permissions);
      expect(actual.enabledPlugins).toEqual(retained.enabledPlugins);
      expect(actual.env.CUSTOM_ENV).toBe("retained");
      expect(actual.env.ANTHROPIC_AUTH_TOKEN).toBe(selected.api_key);
      expect(actual.env.ANTHROPIC_API_KEY).toBe(selected.api_key);
      expect(actual.env.ANTHROPIC_BASE_URL).toBe(selected.base_url);
      expect(actual.env.ANTHROPIC_MODEL).toBe(selected.model);
      expect(actual.env.ANTHROPIC_DEFAULT_HAIKU_MODEL).toBe(selected.model);
      expect(readdirSync(join(root,".claude"))).toEqual(["settings.json"]);
    }
  });

  it("does not create missing settings or change standalone Claude configuration", () => {
    refreshNativeClaudeSettings(sandbox,root);
    expect(readdirSync(root)).toEqual([]);
    mkdirSync(join(root,".claude"));
    const settingsPath=join(root,".claude","settings.json");
    writeFileSync(settingsPath,"standalone content");
    refreshNativeClaudeSettings({},root);
    expect(readFileSync(settingsPath,"utf8")).toBe("standalone content");
  });

  it("rejects malformed retained settings without changing them or exposing credentials", () => {
    save(config());
    const env=nativeModelEnvironment("claude",sandbox,root);
    mkdirSync(join(root,".claude"));
    const settingsPath=join(root,".claude","settings.json");
    for(const content of ["private credential invalid JSON","null",'[]','{"env":[]}']) {
      writeFileSync(settingsPath,content);
      expect(()=>refreshNativeClaudeSettings(env,root)).toThrow(/Native Claude settings (unavailable|invalid)/);
      expect(readFileSync(settingsPath,"utf8")).toBe(content);
      expect(readdirSync(join(root,".claude"))).toEqual(["settings.json"]);
    }
  });

  it("keeps non-platform and non-Run environments unchanged", () => {
    for (const source of [{ OPENAI_API_KEY: "standalone" }, { AGENT_COMPOSE_RUN_ID: "standalone", OPENAI_API_KEY: "standalone" }]) {
      const env = nativeModelEnvironment("codex", source, root);
      expect(env).toEqual(source);
      expect(env).not.toBe(source);
    }
  });

  it("fails closed rather than falling back to stale Sandbox credentials", () => {
    expect(() => nativeModelEnvironment("claude", sandbox, root)).toThrow("configuration unavailable");
    writeFileSync(path, "invalid JSON containing a secret");
    expect(() => nativeModelEnvironment("claude", sandbox, root)).toThrow("configuration unavailable");
    for (const value of [null, {}, { ...config(), task_id: "foreign-task" }, { ...config(), api_key: "" }, { ...config(), base_url: " " }, { ...config(), model: 42 }]) {
      save(value);
      expect(() => nativeModelEnvironment("claude", sandbox, root)).toThrow("configuration is incomplete");
    }
  });

  it("rejects foreign paths, symbolic links, and oversized model files", () => {
    expect(() => nativeModelEnvironment("claude", { ...sandbox, JINGJIAAGENT_TASK_ID: "../foreign" }, root)).toThrow("task correlation unavailable");
    const target = join(root, "foreign.json");
    writeFileSync(target, JSON.stringify(config()));
    symlinkSync(target, path);
    expect(() => nativeModelEnvironment("claude", sandbox, root)).toThrow("configuration unavailable");
    rmSync(path);
    writeFileSync(path, " ".repeat((1 << 20) + 1));
    expect(() => nativeModelEnvironment("claude", sandbox, root)).toThrow("configuration unavailable");
  });
});
