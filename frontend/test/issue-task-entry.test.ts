import assert from "node:assert/strict";
import test from "node:test";
import { initialTaskContent, issueTaskPrompt, issueTaskURL, newTaskDraftKey, parseIssueTaskSource, parseTaskDraft } from "../src/lib/issue-task-entry.ts";

const projectId = "10000000-0000-0000-0000-000000000001";
const issueId = "20000000-0000-0000-0000-000000000002";
const labels = { title: "为需求设计方案", requirement: "原始需求如下：", design: "技术方案如下：" };

test("design and development enter the same composer without exposing document contents in the URL", () => {
  for (const intent of ["design", "develop"] as const) {
    const url = issueTaskURL(projectId, issueId, intent);
    assert.equal(url.split("?")[0], "/console/work");
    assert.deepEqual(parseIssueTaskSource(url.substring(url.indexOf("?"))), { projectId, issueId, intent });
    assert.deepEqual([...new URLSearchParams(url.split("?")[1]).keys()], ["project", "issue", "intent"]);
  }
});

test("incomplete or malformed issue links cannot silently create independent tasks", () => {
  assert.equal(parseIssueTaskSource(`?project=${projectId}`), null);
  for (const search of ["?issue=", `?project=${projectId}&issue=${issueId}`, `?issue=${issueId}&intent=design`, `?project=${projectId}&issue=bad&intent=design`, `?project=${projectId}&issue=${issueId}&intent=other`]) {
    assert.equal(parseIssueTaskSource(search), "invalid");
  }
});

test("editable prompts preserve requirement Markdown and only development includes an existing design", () => {
  const issue = { requirement_document: "需求\n```ts\nconst x = 1;\n```", design_document: "设计稿\n## 数据结构" };
  const design = issueTaskPrompt(issue, "design", labels);
  assert.ok(design.includes(issue.requirement_document));
  assert.ok(!design.includes(issue.design_document));
  const develop = issueTaskPrompt(issue, "develop", labels);
  assert.ok(develop.includes(issue.requirement_document));
  assert.ok(develop.includes(issue.design_document));
  assert.equal(issueTaskPrompt({ design_document: "独立设计稿" }, "develop", labels), `${labels.title}\n\n${labels.design}\n\n独立设计稿`);
});

test("issue drafts are isolated by user, project, issue and intent; edits and intentional clearing win", () => {
  const source = { issueId, intent: "design" as const };
  const keys = [
    newTaskDraftKey("u1", projectId),
    newTaskDraftKey("u1", projectId, source),
    newTaskDraftKey("u2", projectId, source),
    newTaskDraftKey("u1", "other-project", source),
    newTaskDraftKey("u1", projectId, { ...source, issueId: "other-issue" }),
    newTaskDraftKey("u1", projectId, { ...source, intent: "develop" }),
  ];
  assert.equal(new Set(keys).size, keys.length);
  assert.equal(initialTaskContent({ content: "我的修改" }, "原始需求"), "我的修改");
  assert.equal(initialTaskContent({ content: "" }, "原始需求"), "");
  assert.equal(initialTaskContent(null, "原始需求"), "原始需求");
});

test("project drafts reject input saved for a different project, including the independent entry", () => {
  const draft = {
    projectId: "project-b",
    content: "Only for project B",
    skillMention: { id: "skill-b", name: "B skill" },
    branchSelection: { project: "project-b", name: "feature-b" },
  };
  const serialized = JSON.stringify(draft);
  assert.equal(parseTaskDraft(serialized, "project-a"), null);
  assert.equal(parseTaskDraft(serialized, ""), null);
  assert.deepEqual(parseTaskDraft(serialized, "project-b"), draft);
});

test("draft parsing tolerates missing or corrupt storage and preserves intentionally cleared input", () => {
  for (const serialized of [null, "{invalid", "[]", '"text"', "true"]) {
    assert.equal(parseTaskDraft(serialized, projectId), null);
  }
  const saved = { projectId, content: "" };
  assert.deepEqual(parseTaskDraft(JSON.stringify(saved), projectId), saved);
  assert.deepEqual(parseTaskDraft('{"content":"existing input"}', projectId), { content: "existing input" });
});
