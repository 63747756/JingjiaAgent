import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";
import ts from "typescript";
import { MAX_TASK_CONTENT_LENGTH } from "../src/components/console/task/task-content-limit.ts";
import { createLongContentFileName, createLongContentTextFile } from "../src/components/console/task/task-long-content.ts";
import { initialTaskContent, newTaskDraftKey, parseTaskDraft } from "../src/lib/issue-task-entry.ts";

// Execute the composer's actual callbacks; replace storage/network/UI boundaries.
const source = ts.createSourceFile("work-composer.tsx", readFileSync(
  new URL("../src/components/console/work/work-composer.tsx", import.meta.url), "utf8",
), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);

function find(predicate: (node: ts.Node) => boolean): ts.Node {
  let result: ts.Node | undefined;
  function visit(node: ts.Node) {
    if (!result && predicate(node)) result = node;
    if (!result) ts.forEachChild(node, visit);
  }
  visit(source);
  assert.ok(result, "composer expression exists");
  return result;
}

function evaluate(node: ts.Node, context: vm.Context) {
  vm.runInContext(ts.transpileModule(`globalThis.expression = (${node.getText(source)});`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
  }).outputText, context);
  return context.expression;
}

function callback(name: string, context: vm.Context) {
  const node = find(node => (ts.isVariableDeclaration(node) || ts.isFunctionDeclaration(node))
    && node.name?.getText(source) === name);
  if (ts.isFunctionDeclaration(node)) return evaluate(node, context);
  assert.ok(ts.isVariableDeclaration(node) && node.initializer);
  const expression = ts.isCallExpression(node.initializer) && node.initializer.expression.getText(source) === "useCallback"
    ? node.initializer.arguments[0] : node.initializer;
  return evaluate(expression, context);
}

function effect(marker: string, context: vm.Context) {
  const node = find(node => ts.isCallExpression(node) && node.expression.getText(source) === "useEffect"
    && node.arguments[0]?.getText(source).includes(marker)) as ts.CallExpression;
  return evaluate(node.arguments[0], context)();
}

class UploadTooLarge extends Error {}
const original = "  原始资料\n" + "中".repeat(10001) + "\n🚀\n  ";
const plain = (value: unknown) => JSON.parse(JSON.stringify(value));

function fixture(content = original) {
  const storage = new Map<string, string>();
  const files: File[] = [];
  const posts: Array<Record<string, unknown>> = [];
  const notices: string[] = [];
  let id = 0;
  let failStorage = false;
  const draftKey = newTaskDraftKey("owner", "project-a");
  const context = vm.createContext({
    content, currentContentRef: { current: content }, attachments: [], versionIDs: [], resources: [],
    draftKey, loadedDraftKey: draftKey, selectedProject: "project-a", initialContent: "", user: { id: "owner" },
    branchSelection: { project: "project-a", name: "" }, skillMention: null, issueSource: undefined,
    draftScopeRef: { current: { key: draftKey } }, mountedRef: { current: true }, draftSubmitted: { current: false },
    longContentDraft: null, longContentConverting: false, longContentPromptSuppressed: false, isComposing: false,
    submitting: false, uploading: false, uploadBusy: { current: false }, contentRef: { current: { focus() {} } },
    requestID: { current: "initial-request" }, requestSignature: { current: "" },
    config: { enabled: true }, profileID: "profile", modelID: "model", selectedAssistant: { id: "profile" },
    availableProfiles: [{ id: "profile" }], selectedModel: { id: "model" }, modelsLoading: false,
    project: { id: "project-a" }, branch: "", effectiveTeamID: "team", selectedTask: undefined,
    skillsLoading: false, skillIssues: [],
    MAX_TASK_CONTENT_LENGTH, MAX_TASK_UPLOAD_FILES: 10, MAX_TASK_UPLOAD_TOTAL_BYTES: 256 * 1024 * 1024,
    MAX_TASK_UPLOAD_FILE_SIZE_LABEL: "32MB", TaskUploadFileTooLargeError: UploadTooLarge,
    createLongContentFileName, createLongContentTextFile, parseTaskDraft, initialTaskContent,
    workRequestID: () => `unique-${++id}`, workError: (error: Error) => error.message,
    t: (key: string) => key, toast: { success: (message: string) => notices.push(message), error: (message: string) => notices.push(message) },
    localStorage: {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => { if (failStorage) throw new Error("quota"); storage.set(key, value); },
      removeItem: (key: string) => { if (failStorage) throw new Error("quota"); storage.delete(key); },
    },
    uploadTaskFile: async (file: File) => {
      files.push(file);
      if (file.size > 32 * 1024 * 1024) throw new UploadTooLarge();
      return { accessUrl: `/api/v1/assets?key=${file.name}`, name: file.name, size: file.size };
    },
    workPost: async (url: string, input: Record<string, unknown>) => {
      assert.equal(url, "/api/v1/users/work-tasks");
      posts.push(plain(input));
      return { id: "created-task" };
    },
    reloadProjects() {}, reloadUnlinkedTasks() {}, reloadHistoricalTasks() {}, navigate() {},
    window: { dispatchEvent() {} }, CustomEvent: class { constructor(public name: string, public options: unknown) {} },
  });
  for (const name of ["content", "attachments", "skillMention", "branchSelection", "loadedDraftKey", "error",
    "longContentDraft", "longContentConverting", "longContentPromptSuppressed", "submitting", "uploading"]) {
    context[`set${name[0].toUpperCase()}${name.slice(1)}`] = (value: unknown) => {
      context[name] = typeof value === "function" ? value(context[name]) : value;
      if (name === "content") context.currentContentRef.current = context.content;
    };
  }
  Object.defineProperty(context, "inputLocked", { get: () => context.submitting || context.longContentConverting });
  for (const name of ["readTaskDraft", "writeTaskDraft", "handleContentChange", "openLongContentDialog",
    "handleLongContentDialogOpenChange", "handleManualLongContentConversion", "handleConfirmLongContentConversion", "submit"]) {
    context[name] = callback(name, context);
  }
  const save = () => effect("if (!user.id || draftSubmitted.current) return", context);
  save();
  return {
    context, storage, files, posts, notices, save,
    prompt: () => effect("if (content.length <= MAX_TASK_CONTENT_LENGTH)", context),
    confirm: (): Promise<void> => context.handleConfirmLongContentConversion(),
    failStorage: () => { failStorage = true; },
  };
}

test("new tasks use the 10000 character boundary for typed, restored and initial requirement text, after IME ends", () => {
  const f = fixture("x".repeat(10000));
  f.prompt();
  assert.equal(f.context.longContentDraft, null);
  f.context.handleContentChange("x".repeat(10001));
  f.context.isComposing = true;
  f.prompt();
  assert.equal(f.context.longContentDraft, null);
  f.context.isComposing = false;
  f.prompt();
  assert.equal(f.context.longContentDraft.content.length, 10001);
  const loaded = fixture();
  loaded.prompt(); // No onChange is needed for restored or issue-provided input.
  assert.equal(loaded.context.longContentDraft.content, original);
});

test("cancel keeps the full text, suppresses repeated prompts and allows manual conversion", () => {
  const f = fixture();
  f.prompt();
  const firstName = f.context.longContentDraft.filename;
  f.context.handleLongContentDialogOpenChange(false);
  f.prompt();
  assert.equal(f.context.content, original);
  assert.equal(f.context.longContentDraft, null);
  f.context.handleManualLongContentConversion();
  assert.equal(f.context.longContentDraft.content, original);
  assert.notEqual(f.context.longContentDraft.filename, firstName, "each upload must have a unique object key");
  f.context.handleLongContentDialogOpenChange(false);
  f.context.handleContentChange("short"); f.prompt();
  f.context.handleContentChange(original); f.prompt();
  assert.ok(f.context.longContentDraft, "crossing the threshold again may prompt again");
});

test("conversion preserves exact UTF-8 content and durably restores its attachment before clearing the text", async () => {
  const f = fixture();
  f.prompt();
  await f.confirm();
  assert.equal(f.files.length, 1);
  assert.equal(await f.files[0].text(), original);
  assert.equal(f.files[0].type, "text/plain;charset=utf-8");
  assert.equal(f.context.content, "");
  assert.equal(f.context.attachments.length, 1);
  const restored = f.context.readTaskDraft(f.context.draftKey, "project-a");
  assert.equal(restored.content, "");
  assert.deepEqual(plain(restored.attachments), plain(f.context.attachments));
  assert.equal(f.context.readTaskDraft(f.context.draftKey, "project-b"), null);
  assert.ok(f.notices.includes("taskDetail.chat.longContent.addDescription"));
  await f.context.submit();
  assert.equal(f.posts.length, 0, "the attachment needs a request or skill mention before sending");
  f.context.handleContentChange("请按附件要求完成任务");
  f.save();
  f.context.workPost = async () => { throw new Error("offline"); };
  await f.context.submit();
  assert.equal(f.context.content, "请按附件要求完成任务");
  assert.equal(f.context.readTaskDraft(f.context.draftKey, "project-a").attachments.length, 1);
  f.context.workPost = async (_url: string, input: Record<string, unknown>) => { f.posts.push(plain(input)); return { id: "task" }; };
  await f.context.submit();
  assert.equal(f.posts[0].content, "请按附件要求完成任务");
  assert.deepEqual(f.posts[0].attachments, plain(f.context.attachments.map(({ url, filename }: { url: string; filename: string }) => ({ url, filename }))));
  assert.equal(f.storage.has(f.context.draftKey), false, "only accepted creation clears the draft");
});

test("upload and durable draft failures preserve the original text and allow retry", async () => {
  for (const cause of ["network", "oversize", "storage"]) {
    const f = fixture();
    f.prompt();
    if (cause === "storage") f.failStorage();
    else f.context.uploadTaskFile = async () => { throw cause === "oversize" ? new UploadTooLarge() : new Error("network"); };
    await f.confirm();
    assert.equal(f.context.content, original);
    assert.equal(f.context.attachments.length, 0);
    assert.equal(f.context.uploadBusy.current, false);
    assert.equal(f.context.longContentConverting, false);
    assert.ok(f.context.error);
    assert.equal(JSON.parse(f.storage.get(f.context.draftKey)!).content, original);
  }
});

test("conversion respects combined attachment count and byte limits before replacing the input", async () => {
  const full = fixture();
  full.context.versionIDs = Array.from({ length: 10 }, (_, i) => `version-${i}`);
  full.prompt();
  assert.equal(full.context.longContentDraft, null);
  assert.equal(full.context.content, original);
  assert.equal(full.files.length, 0);
  const bytes = fixture();
  bytes.context.resources = [{ versions: [{ id: "version", size: 256 * 1024 * 1024 }] }];
  bytes.context.versionIDs = ["version"];
  bytes.prompt();
  await bytes.confirm();
  assert.equal(bytes.files.length, 0);
  assert.equal(bytes.context.content, original);
  assert.equal(bytes.context.error, "assistantTasks.uploadTotalLimit");
});

test("duplicate confirmation and navigation cannot apply an old upload to a different draft generation", async () => {
  for (const navigation of ["unmount", "project-return"]) {
    const f = fixture();
    let finish!: (file: { accessUrl: string; name: string; size: number }) => void;
    let uploads = 0;
    f.context.uploadTaskFile = () => { uploads++; return new Promise(resolve => { finish = resolve; }); };
    f.prompt();
    const pending = f.confirm();
    await f.confirm();
    assert.equal(uploads, 1);
    if (navigation === "unmount") f.context.mountedRef.current = false;
    else f.context.draftScopeRef.current = { key: f.context.draftKey }; // A -> B -> A, same key but new generation.
    finish({ accessUrl: "/api/v1/assets?key=old", name: "old.txt", size: 100 });
    await pending;
    assert.equal(f.context.content, original);
    assert.equal(f.context.attachments.length, 0);
    assert.equal(JSON.parse(f.storage.get(f.context.draftKey)!).content, original);
  }
});

test("programmatically updated text during upload is not cleared by the old conversion", async () => {
  const f = fixture();
  let finish!: (file: { accessUrl: string; name: string; size: number }) => void;
  f.context.uploadTaskFile = () => new Promise(resolve => { finish = resolve; });
  f.prompt();
  const pending = f.confirm();
  f.context.currentContentRef.current = "新增说明";
  finish({ accessUrl: "/api/v1/assets?key=file", name: "file.txt", size: 100 });
  await pending;
  assert.equal(f.context.content, "新增说明");
  assert.equal(f.context.readTaskDraft(f.context.draftKey, "project-a").content, "新增说明");
});

test("submit rejects oversized new-task text even when called without clicking the button", async () => {
  const f = fixture();
  await f.context.submit();
  assert.equal(f.posts.length, 0);
  assert.equal(f.context.content, original);
  assert.equal(f.context.error, "taskWorkflow.input.contentTooLong");
  const allowed = fixture("x".repeat(10000));
  await allowed.context.submit();
  assert.equal(allowed.posts.length, 1);
  assert.equal((allowed.posts[0].content as string).length, 10000);
  assert.equal(allowed.files.length, 0, "ordinary input remains inline");
});

test("project draft switching restores attachments together with content instead of carrying the old project's files", () => {
  const f = fixture();
  const file = { url: "/api/v1/assets?key=b", filename: "b.txt", size: 123 };
  const keyB = newTaskDraftKey("owner", "project-b");
  f.storage.set(keyB, JSON.stringify({ content: "B", projectId: "project-b", attachments: [file] }));
  f.context.selectedProject = "project-b";
  f.context.draftKey = keyB;
  f.save();
  assert.equal(f.context.content, "B");
  assert.deepEqual(plain(f.context.attachments), [file]);
  assert.equal(f.context.loadedDraftKey, keyB);
  f.context.selectedProject = "project-a";
  f.context.draftKey = newTaskDraftKey("owner", "project-a");
  f.save();
  assert.equal(f.context.content, original);
  assert.deepEqual(plain(f.context.attachments), []);
});
