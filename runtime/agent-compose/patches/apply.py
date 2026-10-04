"""Small, explicit patches against source.lock.json; fail if upstream context differs."""
import argparse
import hashlib
import json
from pathlib import Path


def replace_once(root, name, before, after):
    path = root / name
    text = path.read_text(encoding="utf-8")
    if after in text:
        return
    if text.count(before) != 1:
        raise SystemExit("Pinned patch context does not match: " + name)
    path.write_text(text.replace(before, after), encoding="utf-8", newline="\n")


def apply(root):
    for name in ['monkeycode_interruption.go', 'monkeycode_interruption_test.go']:
        (root / 'pkg/runs' / name).write_text((Path(__file__).parent / name).read_text(encoding='utf-8'), encoding='utf-8', newline='\n')
    (root / 'pkg/runs/monkeycode_interruption_integration_test.go').write_text((Path(__file__).parent / 'monkeycode_interruption_integration_test.go').read_text(encoding='utf-8'), encoding='utf-8', newline='\n')
    replace_once(root, 'pkg/runs/completion.go',
                 'func (m *CompletionManager) StageInterrupted(ctx context.Context, run domain.ProjectRunRecord, message string) error {\n\treq :=',
                 'func (m *CompletionManager) StageInterrupted(ctx context.Context, run domain.ProjectRunRecord, message string) error {\n\t// List summaries omit labels; use the durable detail before selecting ownership cleanup.\n\tcurrent, err := m.store.GetProjectRun(ctx, run.RunID)\n\tif err != nil { return err }\n\trun = current\n\treq :=')
    replace_once(root, 'pkg/runs/completion.go',
                 'CleanupAction: CompletionCleanupAction(run.CleanupPolicy, strings.TrimSpace(run.SandboxID) != "", run.SandboxCreated),\n\t}, nil)',
                 'CleanupAction: monkeyCodeInterruptedCleanupAction(run),\n\t}, nil)')
    (root / 'runtime/javascript/src/monkeycode-assets.ts').write_text((Path(__file__).parent / 'monkeycode_assets.ts').read_text(encoding='utf-8'), encoding='utf-8', newline='\n')
    replace_once(root, 'runtime/javascript/src/runners/claude.ts',
                 '    const { query: claudeQuery } = await import("@anthropic-ai/claude-agent-sdk");',
                 '    if (process.env.AGENT_COMPOSE_RUN_ID) Object.assign(this.options, nativeAssets(this.options, "claude"));\n    const { query: claudeQuery } = await import("@anthropic-ai/claude-agent-sdk");')
    replace_once(root, 'runtime/javascript/src/runners/claude.ts',
                 '      prompt: promptText,',
                 '      prompt: process.env.AGENT_COMPOSE_RUN_ID ? claudePrompt(promptText) : promptText,')
    (root / 'runtime/javascript/src/monkeycode-codex.ts').write_text((Path(__file__).parent / 'monkeycode_codex.ts').read_text(encoding='utf-8'), encoding='utf-8', newline='\n')
    replace_once(root, 'runtime/javascript/src/runners/codex.ts',
                 'import { codexTelemetryConfig, providerTelemetryEnv } from "../telemetry.js";',
                 'import { codexTelemetryConfig, providerTelemetryEnv } from "../telemetry.js";\nimport { nativeCodex } from "../monkeycode-codex.js";')
    replace_once(root, 'runtime/javascript/src/runners/codex.ts',
                 '  async runPrompt(promptText: string): Promise<AgentResult> {',
                 '  async runPrompt(promptText: string): Promise<AgentResult> {\n    if (process.env.AGENT_COMPOSE_RUN_ID) return nativeCodex(this.options, promptText, event => this.emit(event));')
    (root / 'runtime/javascript/src/monkeycode-sdk.ts').write_text((Path(__file__).parent / 'monkeycode_sdk.ts').read_text(encoding='utf-8'), encoding='utf-8', newline='\n')
    replace_once(root, 'runtime/javascript/src/runners/claude.ts',
                 'import { flattenEnvMap } from "../mcp-config.js";',
                 'import { flattenEnvMap } from "../mcp-config.js";\nimport { nativeModelEnvironment } from "../monkeycode-sdk.js";')
    replace_once(root, 'runtime/javascript/src/runners/claude.ts',
                 '  return env;\n}',
                 '  return nativeModelEnvironment("claude", env);\n}')
    replace_once(root, 'runtime/javascript/src/runners/claude.ts',
                 'import { providerTelemetryEnv } from "../telemetry.js";',
                 'import { providerTelemetryEnv } from "../telemetry.js";\nimport { claudeToolDecision } from "../monkeycode-sdk.js";')
    replace_once(root, 'runtime/javascript/src/runners/claude.ts',
                 'import { claudeToolDecision } from "../monkeycode-sdk.js";',
                 'import { claudeToolDecision } from "../monkeycode-sdk.js";\nimport { nativeAssets, claudePrompt } from "../monkeycode-assets.js";')
    replace_once(root, 'runtime/javascript/src/runners/claude.ts',
                 '      ...(mcpServers ? {\n        mcpServers,\n        strictMcpConfig: true,\n      } : {}),',
                 '      mcpServers: mcpServers || {},\n      strictMcpConfig: true,')
    replace_once(root, 'runtime/javascript/src/runners/claude.ts',
                 '      permissionMode: "bypassPermissions",\n      allowDangerouslySkipPermissions: true,',
                 '      permissionMode: "default",\n      canUseTool: (tool: string, input: Record<string, unknown>, options: any) => claudeToolDecision(tool, input, options, event => this.emit(event)),')
    for name in ['monkeycode_recycled.go', 'monkeycode_recycled_test.go']:
        (root / 'pkg/sandboxes' / name).write_text((Path(__file__).parent / name).read_text(encoding='utf-8'), encoding='utf-8', newline='\n')
    removal_path = root / 'pkg/sandboxes/removal.go'
    removal = removal_path.read_text(encoding='utf-8')
    obsolete = '\t\tif os.IsNotExist(sandboxErr) && monkeyCodeRecycled(c.SandboxRoot, sandboxID) { return RemovalResult{SandboxID: sandboxID, Removed: true}, nil }\n'
    if obsolete in removal:
        removal_path.write_text(removal.replace(obsolete, ''), encoding='utf-8', newline='\n')
    replace_once(root, 'pkg/sandboxes/removal.go',
                 '\t\tif sandboxErr != nil {\n\t\t\treturn RemovalResult{}, fmt.Errorf("%w: sandbox %s has neither record nor metadata", ErrOwnershipUnknown, sandboxID)',
                 '\t\tif errors.Is(sandboxErr, os.ErrNotExist) && monkeyCodeRecycled(c.SandboxRoot, sandboxID) { return RemovalResult{SandboxID: sandboxID, Removed: true}, nil }\n\t\tif sandboxErr != nil {\n\t\t\treturn RemovalResult{}, fmt.Errorf("%w: sandbox %s has neither record nor metadata", ErrOwnershipUnknown, sandboxID)')
    replace_once(root, 'pkg/sandboxes/removal.go',
                 '\tif err := RemoveOwnershipRecord(c.SandboxRoot, sandboxID); err != nil {',
                 '\tif err := monkeyCodeWriteRecycled(c.SandboxRoot, sandboxID); err != nil { return result, err }\n\tif err := RemoveOwnershipRecord(c.SandboxRoot, sandboxID); err != nil {')
    preview_registration = '\tproxy.RegisterMonkeyCodePreview(app, do.MustInvoke[*sandboxstore.Store](di), do.MustInvoke[*appconfig.Config](di).DaemonAuthToken)\n'
    # Later registrations separate this line from schedulerController. Detect
    # the stable registration itself so upgrading an already patched checkout
    # remains idempotent without weakening the pinned source-context check.
    if preview_registration not in (root / "pkg/agentcompose/app/app.go").read_text(encoding="utf-8"):
        replace_once(root, "pkg/agentcompose/app/app.go",
                     '\tapp := do.MustInvoke[*echo.Echo](di)\n\tschedulerController :=',
                     '\tapp := do.MustInvoke[*echo.Echo](di)\n' + preview_registration + '\tschedulerController :=')
    (root / "pkg/agentcompose/proxy/monkeycode_preview.go").write_text(
        (Path(__file__).parent / "monkeycode_preview.go").read_text(encoding="utf-8"), encoding="utf-8", newline="\n")
    replace_once(root, "pkg/agentcompose/app/app.go",
                 '\tproxy.RegisterMonkeyCodePreview(app, do.MustInvoke[*sandboxstore.Store](di), do.MustInvoke[*appconfig.Config](di).DaemonAuthToken)\n',
                 '\tproxy.RegisterMonkeyCodePreview(app, do.MustInvoke[*sandboxstore.Store](di), do.MustInvoke[*appconfig.Config](di).DaemonAuthToken)\n'
                 '\tproxy.RegisterMonkeyCodeNode(app, do.MustInvoke[*appconfig.Config](di).DataRoot, do.MustInvoke[*appconfig.Config](di).DaemonAuthToken)\n')
    (root / "pkg/agentcompose/proxy/monkeycode_node.go").write_text(
        (Path(__file__).parent / "monkeycode_node.go").read_text(encoding="utf-8"), encoding="utf-8", newline="\n")
    (root / "pkg/agentcompose/proxy/monkeycode_node_test.go").write_text(
        (Path(__file__).parent / "monkeycode_node_test.go").read_text(encoding="utf-8"), encoding="utf-8", newline="\n")
    replace_once(root, "pkg/runs/controller.go",
                 '\tagentConfig, err := c.projectRunAgentConfig(ctx, run)\n\tif err != nil {\n\t\trun, markErr := c.completeProjectRun(sandboxed.TransitionCtx, TransitionRequest{\n\t\t\tRunID:     run.RunID,\n\t\t\tStatus:    domain.ProjectRunStatusFailed,\n\t\t\tSandboxID: sandboxed.Sandbox.Summary.ID,\n\t\t\tExitCode:  1,\n\t\t\tError:     fmt.Sprintf("agent execution failed: %v", err),\n\t\t})',
                 '\tagentConfig, err := c.projectRunAgentConfig(ctx, run)\n\tif err != nil {\n\t\trun, markErr := c.completeProjectRunError(sandboxed.TransitionCtx, ctx, TransitionRequest{\n\t\t\tRunID:     run.RunID,\n\t\t\tSandboxID: sandboxed.Sandbox.Summary.ID,\n\t\t\tExitCode:  1,\n\t\t\tError:     fmt.Sprintf("agent execution failed: %v", err),\n\t\t}, err)')
    replace_once(root, "runtime/javascript/src/prompt.ts",
                 'import path from "node:path";',
                 'import path from "node:path";\nimport { mkdirSync, appendFileSync } from "node:fs";\nimport type { AgentEvent } from "./agent-event.js";')
    replace_once(root, "runtime/javascript/src/prompt.ts",
                 '    abortController: commandOptions.abortController,',
                 '''    abortController: commandOptions.abortController,
    onEvent: process.env.AGENT_COMPOSE_RUN_ID ? (event: AgentEvent) => {
      const runID = process.env.AGENT_COMPOSE_RUN_ID!;
      if (!/^[a-zA-Z0-9_-]+$/.test(runID)) throw new Error("invalid Run ID");
      const directory = path.join(stateRoot, "monkeycode-events");
      mkdirSync(directory, { recursive: true, mode: 0o700 });
      appendFileSync(path.join(directory, runID + ".jsonl"), JSON.stringify(event) + "\\n", { mode: 0o600, flush: true });
    } : undefined,''')
    replace_once(root, "pkg/agentcompose/adapters/agent_runner.go",
                 '\tresult, err := runtime.ExecStream(ctx, session, vmState, spec, stream)',
                 '\tfinishActivity := r.monkeyCodeActivity(ctx, session, runID)\n'
                 '\tresult, err := runtime.ExecStream(ctx, session, vmState, spec, stream)\n'
                 '\tif activityErr := finishActivity(); activityErr != nil && err == nil { err = activityErr }')
    (root / "pkg/agentcompose/adapters/monkeycode_activity.go").write_text(
        (Path(__file__).parent / "monkeycode_activity.go").read_text(encoding="utf-8"), encoding="utf-8", newline="\n")
    replace_once(root, "pkg/runs/prompt_projection.go",
                 '\tcase "agent_event":\n\t\tname, text := p.agentEventText(frame.Event)',
                 '\tcase "agent_event":\n'
                 '\t\t// MonkeyCode: persist deltas before publishing them so polling/reconnects retain live output.\n'
                 '\t\tif p.events != nil {\n'
                 '\t\t\t_, _, err := p.events.AppendProjectRunEvent(p.eventContext(), domain.ProjectRunEventRecord{\n'
                 '\t\t\t\tID: attachedAgentEventID(p.run.RunID, frame.Seq, line), RunID: p.run.RunID,\n'
                 '\t\t\t\tKind: domain.ProjectRunEventKindAgentActivity, Agent: p.run.AgentName, PayloadJSON: string(frame.Event),\n'
                 '\t\t\t})\n'
                 '\t\t\tif err != nil { return nil, nil, err }\n'
                 '\t\t}\n'
                 '\t\tname, text := p.agentEventText(frame.Event)')
    replace_once(root, "pkg/runs/controller_completion.go",
                 '\ttransition.Status = domain.ProjectRunStatusFailed\n\tif errors.Is(err, context.Canceled) {',
                 '\ttransition.Status = domain.ProjectRunStatusFailed\n'
                 '\t// MonkeyCode: driver/RPC boundaries may serialize a cancellation error.\n'
                 '\t// Classify it using the actual execution context, never error text.\n'
                 '\tif errors.Is(err, context.Canceled) || errors.Is(executionCtx.Err(), context.Canceled) {')
    replace_once(root, "runtime/javascript/src/runners/opencode.ts",
                 '      "--dangerously-skip-permissions",',
                 '      // MonkeyCode: retain native permission decisions; no blanket approval.')
    replace_once(root, "runtime/javascript/src/runners/opencode.ts",
                 '      const child = spawn("opencode", this.buildArgs(promptText, stored), {',
                 '''      const nativeBridge = process.env.AGENT_COMPOSE_RUN_ID;
      const child = spawn(nativeBridge ? "python3" : "opencode",
        nativeBridge ? ["/opt/agent-compose-runtime/monkeycode-opencode.py", ...this.buildArgs(promptText, stored)] : this.buildArgs(promptText, stored), {''')
    if 'case "monkeycode_permission_reply":' not in (root / 'runtime/javascript/src/runners/opencode.ts').read_text(encoding='utf-8'):
        replace_once(root, "runtime/javascript/src/runners/opencode.ts",
                 '    switch (type) {\n      case "step_start":',
                 '''    switch (type) {
      case "monkeycode_permission_reply": {
        const interaction = isRecord(event.interaction) ? event.interaction : {};
        this.emit({kind: "tool_call", id: String(interaction.request_id || ""),
          name: "monkeycode_permission_reply", toolKind: "other", status: "completed", input: interaction});
        return;
      }
      case "monkeycode_interaction": {
        const interaction = isRecord(event.interaction) ? event.interaction : {};
        this.emit({kind: "tool_call", id: String(interaction.request_id || ""),
          name: "question", toolKind: "other", status: "pending", input: interaction});
        return;
      }
      case "step_start":''')
    replace_once(root, "runtime/javascript/src/runners/opencode.ts",
                 '    switch (type) {\n      case "monkeycode_permission_reply":',
                 '''    switch (type) {
      case "monkeycode_text_delta":
      case "monkeycode_reasoning_delta":
        if (typeof part.text === "string" && part.text) {
          this.emit({kind: type === "monkeycode_text_delta" ? "text_delta" : "reasoning_delta", step: this.step, text: part.text});
        }
        return;
      case "monkeycode_permission_reply":''')
    replace_once(root, "runtime/javascript/src/runners/opencode.ts",
                 '        if (typeof part.text === "string" && part.text) {\n          this.emit({ kind: "text_delta",',
                 '        if (!event.monkeycode_streamed && typeof part.text === "string" && part.text) {\n          this.emit({ kind: "text_delta",')
    replace_once(root, "runtime/javascript/src/runners/opencode.ts",
                 '        if (typeof part.text === "string" && part.text) {\n          this.emit({ kind: "reasoning_delta",',
                 '        if (!event.monkeycode_streamed && typeof part.text === "string" && part.text) {\n          this.emit({ kind: "reasoning_delta",')
    replace_once(root, "runtime/javascript/src/runners/opencode.ts",
                 '    if (text) {\n      this.writer.write(text);',
                 '    if (text && !event.monkeycode_streamed) {\n      this.writer.write(text);')
    (root / 'runtime/javascript/test/monkeycode-stream.test.ts').write_text((Path(__file__).parent / 'opencode_stream.test.ts').read_text(encoding='utf-8'), encoding='utf-8', newline='\n')
    replace_once(root, "pkg/driver/docker_runtime.go",
                 '\thostConfig := dockerSandboxHostConfig(mounts, portBindings, networkMode)\n',
                 '\thostConfig := dockerSandboxHostConfig(mounts, portBindings, networkMode)\n'
                 '\tif err := monkeyCodeDockerResources(hostConfig, sandbox); err != nil {\n'
                 '\t\treturn containerapi.InspectResponse{}, false, err\n\t}\n')
    (root / "pkg/driver/monkeycode_resources.go").write_text('''package driver

import (
    "fmt"
    "math"
    "strconv"
    containerapi "github.com/docker/docker/api/types/container"
)

// These values are assigned by the MonkeyCode control plane, after merging
// user configuration. They become Docker cgroup limits before container start.
func monkeyCodeDockerResources(host *containerapi.HostConfig, sandbox *Sandbox) error {
    cpu, memory := "", ""
    for _, item := range sandbox.EnvItems {
        switch item.Name {
        case "MONKEYCODE_SANDBOX_CPUS": cpu = item.Value
        case "MONKEYCODE_SANDBOX_MEMORY": memory = item.Value
        }
    }
    if cpu == "" && memory == "" { return nil }
    cores, err := strconv.ParseFloat(cpu, 64)
    if err != nil || math.IsNaN(cores) || math.IsInf(cores, 0) || cores <= 0 || cores > 256 {
        return fmt.Errorf("invalid MonkeyCode CPU limit")
    }
    bytes, err := strconv.ParseInt(memory, 10, 64)
    if err != nil || bytes < 64 << 20 { return fmt.Errorf("invalid MonkeyCode memory limit") }
    host.NanoCPUs = int64(cores * 1e9)
    host.Memory = bytes
    host.MemorySwap = bytes
    pids := int64(1024)
    host.PidsLimit = &pids
    return nil
}
''', encoding="utf-8", newline="\n")
    name = "pkg/llms/runtime_config.go"
    source = (root / name).read_text(encoding="utf-8")
    start = source.index("func WriteOpenCodeRuntimeConfig(")
    end = source.index("// openCodeProviderPackage", start)
    section = source[start:end]
    marker = "\t// MonkeyCode: preserve rules, plugins, permissions and model options.\n"
    if marker not in section:
        before = '\tdata, err := json.MarshalIndent(payload, "", "  ")'
        after = marker + '''\tif previous, readErr := os.ReadFile(path); readErr == nil {
        existing := map[string]any{}
        if err := json.Unmarshal(previous, &existing); err != nil { return fmt.Errorf("invalid existing opencode config: %w", err) }
        generated := payload["provider"].(map[string]any)
        if providers, ok := existing["provider"].(map[string]any); ok {
            if original, ok := providers["monkeycode-ai"].(map[string]any); ok {
                if models, ok := original["models"].(map[string]any); ok {
                    if settings, ok := models[model]; ok {
                        generated[GuestProviderAgentCompose].(map[string]any)["models"] = map[string]any{model: settings}
                    }
                }
            }
            for key, value := range generated { providers[key] = value }
            payload["provider"] = providers
        }
        for key, value := range existing { if key != "provider" && key != "model" { payload[key] = value } }
    } else if !os.IsNotExist(readErr) { return fmt.Errorf("read existing opencode config: %w", readErr) }
    payload["model"] = GuestProviderAgentCompose + "/" + model
''' + before
        if section.count(before) != 1:
            raise SystemExit("Pinned OpenCode config patch context changed")
        source = source[:start] + section.replace(before, after) + source[end:]
        (root / name).write_text(source, encoding="utf-8", newline="\n")
    files = ["pkg/sandboxes/removal.go", "pkg/sandboxes/monkeycode_recycled.go", "pkg/agentcompose/app/app.go", "pkg/agentcompose/proxy/monkeycode_preview.go", "pkg/agentcompose/proxy/monkeycode_node.go", "runtime/javascript/src/runners/opencode.ts", "pkg/driver/docker_runtime.go",
             "pkg/driver/monkeycode_resources.go", "pkg/llms/runtime_config.go", "pkg/runs/controller_completion.go", "pkg/runs/prompt_projection.go",
             "pkg/runs/controller.go", "runtime/javascript/src/prompt.ts", "pkg/agentcompose/adapters/agent_runner.go", "pkg/agentcompose/adapters/monkeycode_activity.go", "runtime/javascript/src/monkeycode-sdk.ts", "runtime/javascript/src/runners/claude.ts", "runtime/javascript/src/monkeycode-codex.ts", "runtime/javascript/src/runners/codex.ts"]
    print(json.dumps({"patched_files": {name: hashlib.sha256((root / name).read_bytes()).hexdigest() for name in files}}, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("source", type=Path)
    apply(parser.parse_args().source.resolve())
