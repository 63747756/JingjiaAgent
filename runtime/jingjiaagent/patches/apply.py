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
    for name in ['jingjiaagent_interruption.go', 'jingjiaagent_interruption_test.go']:
        (root / 'pkg/runs' / name).write_text((Path(__file__).parent / name).read_text(encoding='utf-8'), encoding='utf-8', newline='\n')
    (root / 'pkg/runs/jingjiaagent_interruption_integration_test.go').write_text((Path(__file__).parent / 'jingjiaagent_interruption_integration_test.go').read_text(encoding='utf-8'), encoding='utf-8', newline='\n')
    replace_once(root, 'pkg/runs/completion.go',
                 'func (m *CompletionManager) StageInterrupted(ctx context.Context, run domain.ProjectRunRecord, message string) error {\n\treq :=',
                 'func (m *CompletionManager) StageInterrupted(ctx context.Context, run domain.ProjectRunRecord, message string) error {\n\t// List summaries omit labels; use the durable detail before selecting ownership cleanup.\n\tcurrent, err := m.store.GetProjectRun(ctx, run.RunID)\n\tif err != nil { return err }\n\trun = current\n\treq :=')
    replace_once(root, 'pkg/runs/completion.go',
                 'CleanupAction: CompletionCleanupAction(run.CleanupPolicy, strings.TrimSpace(run.SandboxID) != "", run.SandboxCreated),\n\t}, nil)',
                 'CleanupAction: jingjiaAgentInterruptedCleanupAction(run),\n\t}, nil)')
    (root / 'runtime/javascript/src/jingjiaagent-assets.ts').write_text((Path(__file__).parent / 'jingjiaagent_assets.ts').read_text(encoding='utf-8'), encoding='utf-8', newline='\n')
    replace_once(root, 'runtime/javascript/src/runners/claude.ts',
                 '    const { query: claudeQuery } = await import("@anthropic-ai/claude-agent-sdk");',
                 '    if (process.env.AGENT_COMPOSE_RUN_ID) Object.assign(this.options, nativeAssets(this.options, "claude"));\n    const { query: claudeQuery } = await import("@anthropic-ai/claude-agent-sdk");')
    replace_once(root, 'runtime/javascript/src/runners/claude.ts',
                 '      prompt: promptText,',
                 '      prompt: process.env.AGENT_COMPOSE_RUN_ID ? claudePrompt(promptText) : promptText,')
    (root / 'runtime/javascript/src/jingjiaagent-codex.ts').write_text((Path(__file__).parent / 'jingjiaagent_codex.ts').read_text(encoding='utf-8'), encoding='utf-8', newline='\n')
    replace_once(root, 'runtime/javascript/src/runners/codex.ts',
                 'import { codexTelemetryConfig, providerTelemetryEnv } from "../telemetry.js";',
                 'import { codexTelemetryConfig, providerTelemetryEnv } from "../telemetry.js";\nimport { nativeCodex } from "../jingjiaagent-codex.js";')
    replace_once(root, 'runtime/javascript/src/runners/codex.ts',
                 '  async runPrompt(promptText: string): Promise<AgentResult> {',
                 '  async runPrompt(promptText: string): Promise<AgentResult> {\n    if (process.env.AGENT_COMPOSE_RUN_ID) return nativeCodex(this.options, promptText, event => this.emit(event));')
    (root / 'runtime/javascript/src/jingjiaagent-sdk.ts').write_text((Path(__file__).parent / 'jingjiaagent_sdk.ts').read_text(encoding='utf-8'), encoding='utf-8', newline='\n')
    (root / 'runtime/javascript/test/jingjiaagent-model.test.ts').write_text((Path(__file__).parent / 'jingjiaagent_model.test.ts').read_text(encoding='utf-8'), encoding='utf-8', newline='\n')
    if 'refreshNativeClaudeSettings(current);' not in (root / 'runtime/javascript/src/runners/claude.ts').read_text(encoding='utf-8'):
        replace_once(root, 'runtime/javascript/src/runners/claude.ts',
                     'import { flattenEnvMap } from "../mcp-config.js";',
                     'import { flattenEnvMap } from "../mcp-config.js";\nimport { nativeModelEnvironment } from "../jingjiaagent-sdk.js";')
        replace_once(root, 'runtime/javascript/src/runners/claude.ts',
                     '  return env;\n}',
                     '  return nativeModelEnvironment("claude", env);\n}')
        replace_once(root, 'runtime/javascript/src/runners/claude.ts',
                     'import { nativeModelEnvironment } from "../jingjiaagent-sdk.js";',
                     'import { nativeModelEnvironment, refreshNativeClaudeSettings } from "../jingjiaagent-sdk.js";')
        replace_once(root, 'runtime/javascript/src/runners/claude.ts',
                     '  return nativeModelEnvironment("claude", env);',
                     '  const current = nativeModelEnvironment("claude", env);\n  refreshNativeClaudeSettings(current);\n  return current;')
    replace_once(root, 'runtime/javascript/src/runners/claude.ts',
                 'import { providerTelemetryEnv } from "../telemetry.js";',
                 'import { providerTelemetryEnv } from "../telemetry.js";\nimport { claudeToolDecision } from "../jingjiaagent-sdk.js";')
    replace_once(root, 'runtime/javascript/src/runners/claude.ts',
                 'import { claudeToolDecision } from "../jingjiaagent-sdk.js";',
                 'import { claudeToolDecision } from "../jingjiaagent-sdk.js";\nimport { nativeAssets, claudePrompt } from "../jingjiaagent-assets.js";')
    replace_once(root, 'runtime/javascript/src/runners/claude.ts',
                 '      ...(mcpServers ? {\n        mcpServers,\n        strictMcpConfig: true,\n      } : {}),',
                 '      mcpServers: mcpServers || {},\n      strictMcpConfig: true,')
    replace_once(root, 'runtime/javascript/src/runners/claude.ts',
                 '      permissionMode: "bypassPermissions",\n      allowDangerouslySkipPermissions: true,',
                 '      permissionMode: "default",\n      canUseTool: (tool: string, input: Record<string, unknown>, options: any) => claudeToolDecision(tool, input, options, event => this.emit(event)),')
    for name in ['jingjiaagent_recycled.go', 'jingjiaagent_recycled_test.go']:
        (root / 'pkg/sandboxes' / name).write_text((Path(__file__).parent / name).read_text(encoding='utf-8'), encoding='utf-8', newline='\n')
    removal_path = root / 'pkg/sandboxes/removal.go'
    removal = removal_path.read_text(encoding='utf-8')
    obsolete = '\t\tif os.IsNotExist(sandboxErr) && jingjiaAgentRecycled(c.SandboxRoot, sandboxID) { return RemovalResult{SandboxID: sandboxID, Removed: true}, nil }\n'
    if obsolete in removal:
        removal_path.write_text(removal.replace(obsolete, ''), encoding='utf-8', newline='\n')
    replace_once(root, 'pkg/sandboxes/removal.go',
                 '\t\tif sandboxErr != nil {\n\t\t\treturn RemovalResult{}, fmt.Errorf("%w: sandbox %s has neither record nor metadata", ErrOwnershipUnknown, sandboxID)',
                 '\t\tif errors.Is(sandboxErr, os.ErrNotExist) && jingjiaAgentRecycled(c.SandboxRoot, sandboxID) { return RemovalResult{SandboxID: sandboxID, Removed: true}, nil }\n\t\tif sandboxErr != nil {\n\t\t\treturn RemovalResult{}, fmt.Errorf("%w: sandbox %s has neither record nor metadata", ErrOwnershipUnknown, sandboxID)')
    replace_once(root, 'pkg/sandboxes/removal.go',
                 '\tif err := RemoveOwnershipRecord(c.SandboxRoot, sandboxID); err != nil {',
                 '\tif err := jingjiaAgentWriteRecycled(c.SandboxRoot, sandboxID); err != nil { return result, err }\n\tif err := RemoveOwnershipRecord(c.SandboxRoot, sandboxID); err != nil {')
    preview_registration = '\tproxy.RegisterJingjiaAgentPreview(app, do.MustInvoke[*sandboxstore.Store](di), do.MustInvoke[*appconfig.Config](di).DaemonAuthToken)\n'
    # Later registrations separate this line from schedulerController. Detect
    # the stable registration itself so upgrading an already patched checkout
    # remains idempotent without weakening the pinned source-context check.
    if preview_registration not in (root / "pkg/agentcompose/app/app.go").read_text(encoding="utf-8"):
        replace_once(root, "pkg/agentcompose/app/app.go",
                     '\tapp := do.MustInvoke[*echo.Echo](di)\n\tschedulerController :=',
                     '\tapp := do.MustInvoke[*echo.Echo](di)\n' + preview_registration + '\tschedulerController :=')
    (root / "pkg/agentcompose/proxy/jingjiaagent_preview.go").write_text(
        (Path(__file__).parent / "jingjiaagent_preview.go").read_text(encoding="utf-8"), encoding="utf-8", newline="\n")
    replace_once(root, "pkg/agentcompose/app/app.go",
                 '\tproxy.RegisterJingjiaAgentPreview(app, do.MustInvoke[*sandboxstore.Store](di), do.MustInvoke[*appconfig.Config](di).DaemonAuthToken)\n',
                 '\tproxy.RegisterJingjiaAgentPreview(app, do.MustInvoke[*sandboxstore.Store](di), do.MustInvoke[*appconfig.Config](di).DaemonAuthToken)\n'
                 '\tproxy.RegisterJingjiaAgentNode(app, do.MustInvoke[*appconfig.Config](di).DataRoot, do.MustInvoke[*appconfig.Config](di).DaemonAuthToken)\n')
    (root / "pkg/agentcompose/proxy/jingjiaagent_node.go").write_text(
        (Path(__file__).parent / "jingjiaagent_node.go").read_text(encoding="utf-8"), encoding="utf-8", newline="\n")
    (root / "pkg/agentcompose/proxy/jingjiaagent_node_test.go").write_text(
        (Path(__file__).parent / "jingjiaagent_node_test.go").read_text(encoding="utf-8"), encoding="utf-8", newline="\n")
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
      const directory = path.join(stateRoot, "jingjiaagent-events");
      mkdirSync(directory, { recursive: true, mode: 0o700 });
      appendFileSync(path.join(directory, runID + ".jsonl"), JSON.stringify(event) + "\\n", { mode: 0o600, flush: true });
    } : undefined,''')
    replace_once(root, "pkg/agentcompose/adapters/agent_runner.go",
                 '\tresult, err := runtime.ExecStream(ctx, session, vmState, spec, stream)',
                 '\tfinishActivity := r.jingjiaAgentActivity(ctx, session, runID)\n'
                 '\tresult, err := runtime.ExecStream(ctx, session, vmState, spec, stream)\n'
                 '\tif activityErr := finishActivity(); activityErr != nil && err == nil { err = activityErr }')
    (root / "pkg/agentcompose/adapters/jingjiaagent_activity.go").write_text(
        (Path(__file__).parent / "jingjiaagent_activity.go").read_text(encoding="utf-8"), encoding="utf-8", newline="\n")
    replace_once(root, "pkg/runs/prompt_projection.go",
                 '\tcase "agent_event":\n\t\tname, text := p.agentEventText(frame.Event)',
                 '\tcase "agent_event":\n'
                 '\t\t// JingjiaAgent: persist deltas before publishing them so polling/reconnects retain live output.\n'
                 '\t\tif p.events != nil {\n'
                 '\t\t\t_, _, err := p.events.AppendProjectRunEvent(p.eventContext(), domain.ProjectRunEventRecord{\n'
                 '\t\t\t\tID: attachedAgentEventID(p.run.RunID, frame.Seq, line), RunID: p.run.RunID,\n'
                 '\t\t\t\tKind: domain.ProjectRunEventKindAgentActivity, Agent: p.run.AgentName, PayloadJSON: string(frame.Event),\n'
                 '\t\t\t})\n'
                 '\t\t\tif err != nil { return nil, nil, err }\n'
                 '\t\t}\n'
                 '\t\tname, text := p.agentEventText(frame.Event)')
    # The retained live-output patch persists the activity frame as well as the
    # final answer. Keep the upstream idempotence test aligned with that contract.
    test_path = root / 'pkg/runs/coverage_shape_workflows_test.go'
    tests = test_path.read_text(encoding='utf-8')
    start = tests.index('func TestPromptAttachProjectorPersistsEachFrameIdempotently(')
    end = tests.index('\nfunc ', start + 1)
    section = tests[start:end]
    if 'retry activity frame' not in section:
        section = section.replace('\tturn := []byte(',
            '\tif _, _, err := projector.Project(activity); err != nil {\n'
            '\t\tt.Fatalf("retry activity frame: %v", err)\n\t}\n\tturn := []byte(', 1)
        section = section.replace('len(store.events) != 2', 'len(store.events) != 3')
        section = section.replace('store.events[1]', 'store.events[2]')
        section = section.replace('\tif store.events[2].Kind != domain.ProjectRunEventKindAgentMessage',
            '\tif store.events[1].Kind != domain.ProjectRunEventKindAgentActivity || store.events[1].ID != attachedAgentEventID("run-events", 41, activity) || store.events[1].PayloadJSON == "" {\n'
            '\t\tt.Fatalf("activity event = %#v", store.events[1])\n\t}\n'
            '\tif store.events[2].Kind != domain.ProjectRunEventKindAgentMessage', 1)
        test_path.write_text(tests[:start] + section + tests[end:], encoding='utf-8', newline='\n')
    replace_once(root, "pkg/runs/controller_completion.go",
                 '\ttransition.Status = domain.ProjectRunStatusFailed\n\tif errors.Is(err, context.Canceled) {',
                 '\ttransition.Status = domain.ProjectRunStatusFailed\n'
                 '\t// JingjiaAgent: driver/RPC boundaries may serialize a cancellation error.\n'
                 '\t// Classify it using the actual execution context, never error text.\n'
                 '\tif errors.Is(err, context.Canceled) || errors.Is(executionCtx.Err(), context.Canceled) {')
    replace_once(root, "runtime/javascript/src/runners/opencode.ts",
                 '      "--dangerously-skip-permissions",',
                 '      // JingjiaAgent: retain native permission decisions; no blanket approval.')
    replace_once(root, "runtime/javascript/src/runners/opencode.ts",
                 '      const child = spawn("opencode", this.buildArgs(promptText, stored), {',
                 '''      const nativeBridge = process.env.AGENT_COMPOSE_RUN_ID;
      const child = spawn(nativeBridge ? "python3" : "opencode",
        nativeBridge ? ["/opt/agent-compose-runtime/jingjiaagent-opencode.py", ...this.buildArgs(promptText, stored)] : this.buildArgs(promptText, stored), {''')
    if 'case "jingjiaagent_permission_reply":' not in (root / 'runtime/javascript/src/runners/opencode.ts').read_text(encoding='utf-8'):
        replace_once(root, "runtime/javascript/src/runners/opencode.ts",
                 '    switch (type) {\n      case "step_start":',
                 '''    switch (type) {
      case "jingjiaagent_permission_reply": {
        const interaction = isRecord(event.interaction) ? event.interaction : {};
        this.emit({kind: "tool_call", id: String(interaction.request_id || ""),
          name: "jingjiaagent_permission_reply", toolKind: "other", status: "completed", input: interaction});
        return;
      }
      case "jingjiaagent_interaction": {
        const interaction = isRecord(event.interaction) ? event.interaction : {};
        this.emit({kind: "tool_call", id: String(interaction.request_id || ""),
          name: "question", toolKind: "other", status: "pending", input: interaction});
        return;
      }
      case "step_start":''')
    replace_once(root, "runtime/javascript/src/runners/opencode.ts",
                 '    switch (type) {\n      case "jingjiaagent_permission_reply":',
                 '''    switch (type) {
      case "jingjiaagent_text_delta":
      case "jingjiaagent_reasoning_delta":
        if (typeof part.text === "string" && part.text) {
          this.emit({kind: type === "jingjiaagent_text_delta" ? "text_delta" : "reasoning_delta", step: this.step, text: part.text});
        }
        return;
      case "jingjiaagent_permission_reply":''')
    replace_once(root, "runtime/javascript/src/runners/opencode.ts",
                 '        if (typeof part.text === "string" && part.text) {\n          this.emit({ kind: "text_delta",',
                 '        if (!event.jingjiaagent_streamed && typeof part.text === "string" && part.text) {\n          this.emit({ kind: "text_delta",')
    replace_once(root, "runtime/javascript/src/runners/opencode.ts",
                 '        if (typeof part.text === "string" && part.text) {\n          this.emit({ kind: "reasoning_delta",',
                 '        if (!event.jingjiaagent_streamed && typeof part.text === "string" && part.text) {\n          this.emit({ kind: "reasoning_delta",')
    replace_once(root, "runtime/javascript/src/runners/opencode.ts",
                 '    if (text) {\n      this.writer.write(text);',
                 '    if (text && !event.jingjiaagent_streamed) {\n      this.writer.write(text);')
    (root / 'runtime/javascript/test/jingjiaagent-stream.test.ts').write_text((Path(__file__).parent / 'opencode_stream.test.ts').read_text(encoding='utf-8'), encoding='utf-8', newline='\n')
    replace_once(root, "pkg/driver/docker_runtime.go",
                 '\thostConfig := dockerSandboxHostConfig(mounts, portBindings, networkMode)\n',
                 '\thostConfig := dockerSandboxHostConfig(mounts, portBindings, networkMode)\n'
                 '\tif err := jingjiaAgentDockerResources(hostConfig, sandbox); err != nil {\n'
                 '\t\treturn containerapi.InspectResponse{}, false, err\n\t}\n')
    (root / "pkg/driver/jingjiaagent_resources.go").write_text('''package driver

import (
    "fmt"
    "math"
    "strconv"
    containerapi "github.com/docker/docker/api/types/container"
)

// These values are assigned by the JingjiaAgent control plane, after merging
// user configuration. They become Docker cgroup limits before container start.
func jingjiaAgentDockerResources(host *containerapi.HostConfig, sandbox *Sandbox) error {
    cpu, memory := "", ""
    for _, item := range sandbox.EnvItems {
        switch item.Name {
        case "JINGJIAAGENT_SANDBOX_CPUS": cpu = item.Value
        case "JINGJIAAGENT_SANDBOX_MEMORY": memory = item.Value
        }
    }
    if cpu == "" && memory == "" { return nil }
    cores, err := strconv.ParseFloat(cpu, 64)
    if err != nil || math.IsNaN(cores) || math.IsInf(cores, 0) || cores <= 0 || cores > 256 {
        return fmt.Errorf("invalid JingjiaAgent CPU limit")
    }
    bytes, err := strconv.ParseInt(memory, 10, 64)
    if err != nil || bytes < 64 << 20 { return fmt.Errorf("invalid JingjiaAgent memory limit") }
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
    marker = "\t// JingjiaAgent: preserve rules, plugins, permissions and model options.\n"
    if marker not in section:
        before = '\tdata, err := json.MarshalIndent(payload, "", "  ")'
        after = marker + '''\tif previous, readErr := os.ReadFile(path); readErr == nil {
        existing := map[string]any{}
        if err := json.Unmarshal(previous, &existing); err != nil { return fmt.Errorf("invalid existing opencode config: %w", err) }
        generated := payload["provider"].(map[string]any)
        if providers, ok := existing["provider"].(map[string]any); ok {
            if original, ok := providers["jingjiaagent"].(map[string]any); ok {
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
    files = ["pkg/sandboxes/removal.go", "pkg/sandboxes/jingjiaagent_recycled.go", "pkg/agentcompose/app/app.go", "pkg/agentcompose/proxy/jingjiaagent_preview.go", "pkg/agentcompose/proxy/jingjiaagent_node.go", "runtime/javascript/src/runners/opencode.ts", "pkg/driver/docker_runtime.go",
             "pkg/driver/jingjiaagent_resources.go", "pkg/llms/runtime_config.go", "pkg/runs/controller_completion.go", "pkg/runs/prompt_projection.go",
             "pkg/runs/controller.go", "runtime/javascript/src/prompt.ts", "pkg/agentcompose/adapters/agent_runner.go", "pkg/agentcompose/adapters/jingjiaagent_activity.go", "runtime/javascript/src/jingjiaagent-sdk.ts", "runtime/javascript/src/runners/claude.ts", "runtime/javascript/src/jingjiaagent-codex.ts", "runtime/javascript/src/runners/codex.ts"]
    print(json.dumps({"patched_files": {name: hashlib.sha256((root / name).read_bytes()).hexdigest() for name in files}}, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("source", type=Path)
    apply(parser.parse_args().source.resolve())
