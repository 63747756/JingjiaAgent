import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import ts from "typescript";

// Execute the actual form callbacks with an in-memory API boundary. No model
// credential or request leaves the test, including when reproducing the bug.
const forms = [
  ["user add", "../src/components/console/settings/add-model.tsx", false],
  ["user edit", "../src/components/console/settings/edit-model.tsx", true],
  ["manager add", "../src/components/manager/add-model.tsx", false],
  ["manager edit", "../src/components/manager/edit-model.tsx", true],
] as const;

function fixture(path: string, edit: boolean, baseUrl: string) {
  const source = ts.createSourceFile(path, readFileSync(new URL(path, import.meta.url), "utf8"),
    ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const requests: Array<{ method: string; data: Record<string, unknown> }> = [];
  const errors: string[] = [];
  const stateChanges: unknown[] = [];
  const previousURL = "https://previous.example.invalid/v1";
  const context: Record<string, unknown> = {
    apiToken: " fixture-key ", baseUrl, provider: "OpenAI", selectedModel: "fixture-model",
    model: edit ? { id: "fixture-id", base_url: previousURL, provider: "OpenAI" } : "fixture-model",
    source: undefined, DEFAULT_BASE_URL: "https://unexpected.example.invalid/v1",
    modelProviderList: {}, t: (key: string) => key,
    toast: { error: (message: string) => errors.push(message), success() {}, warning() {} },
    apiRequest: async (method: string, data: Record<string, unknown>, _params: unknown,
      callback: (response: unknown) => unknown) => {
      requests.push({ method, data });
      await callback({ code: 0, data: { models: [{ model: "fixture-model" }] } });
    },
  };
  for (const name of ["setModelListAttempted", "setModelListFetchFailed", "setModelList", "setLoadingModels", "setSaving"]) {
    context[name] = (value: unknown) => stateChanges.push(value);
  }
  function find(predicate: (node: ts.Node) => boolean): ts.Node {
    let found: ts.Node | undefined;
    function visit(node: ts.Node) {
      if (!found && predicate(node)) found = node;
      if (!found) ts.forEachChild(node, visit);
    }
    visit(source);
    assert.ok(found, "form callback/expression exists");
    return found;
  }
  function evaluate(expression: ts.Expression) {
    const compiled = ts.transpileModule(`const action = (${expression.getText(source)});`, {
      compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
    }).outputText;
    return new Function(...Object.keys(context), `${compiled}; return action;`)(...Object.values(context));
  }
  function action(name: string): () => Promise<void> {
    const node = find(node => ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) && node.name.text === name) as ts.VariableDeclaration;
    assert.ok(node.initializer);
    return evaluate(node.initializer);
  }
  function initialURL(): string {
    const node = find(node => ts.isVariableDeclaration(node) && ts.isArrayBindingPattern(node.name) &&
      node.name.elements.some(item => ts.isBindingElement(item) && ts.isIdentifier(item.name) && item.name.text === "baseUrl")) as ts.VariableDeclaration;
    assert.ok(node.initializer && ts.isCallExpression(node.initializer));
    return evaluate(node.initializer.arguments[0]);
  }
  function restoredURL(value?: string): string {
    const node = find(node => ts.isCallExpression(node) && ts.isIdentifier(node.expression) && node.expression.text === "setBaseUrl") as ts.CallExpression;
    context.source = value === undefined ? undefined : { base_url: value };
    context.model = { base_url: value };
    return evaluate(node.arguments[0]);
  }
  return { requests, errors, stateChanges, action, initialURL, restoredURL, previousURL };
}

for (const [name, path, edit] of forms) {
  test(`${name}: absent endpoint stays empty; an explicit saved endpoint is preserved`, () => {
    const f = fixture(path, edit, "");
    assert.equal(f.initialURL(), "");
    assert.equal(f.restoredURL(), "");
    assert.equal(f.restoredURL(""), "");
    assert.equal(f.restoredURL(f.previousURL), f.previousURL);
  });

  test(`${name}: blank or cleared endpoint cannot fetch models or send the key`, async () => {
    for (const baseURL of ["", " \t "]) {
      const f = fixture(path, edit, baseURL);
      await f.action("fetchModelList")();
      assert.deepEqual(f.requests, []);
      assert.deepEqual(f.stateChanges, []);
      assert.match(f.errors[0], /baseUrlRequired$/);
    }
  });

  test(`${name}: a supplied endpoint is passed unchanged after trimming`, async () => {
    const url = "http://internal.example.invalid:8080/v1";
    const f = fixture(path, edit, ` ${url} `);
    await f.action("fetchModelList")();
    assert.equal(f.requests.length, 1);
    assert.equal(f.requests[0].method, "getProviderModelList");
    assert.equal(f.requests[0].data.base_url, url);
    assert.equal(f.requests[0].data.api_key, "fixture-key");
  });

  test(`${name}: saving without an endpoint does not issue a health check`, async () => {
    const f = fixture(path, edit, " ");
    await f.action("handleSave")();
    assert.deepEqual(f.requests, []);
    assert.match(f.errors[0], /baseUrlRequired$/);
  });
}
