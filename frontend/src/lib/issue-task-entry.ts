import type { DomainProjectIssue } from "@/api/Api";

export type IssueTaskIntent = "design" | "develop";
export type IssueTaskSource = { projectId: string; issueId: string; intent: IssueTaskIntent };

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function issueTaskURL(projectId: string, issueId: string, intent: IssueTaskIntent): string {
  return `/console/work?${new URLSearchParams({ project: projectId, issue: issueId, intent })}`;
}

// An invalid issue link must not silently become an unrelated task.
export function parseIssueTaskSource(search: string): IssueTaskSource | "invalid" | null {
  const query = new URLSearchParams(search);
  if (!query.has("issue") && !query.has("intent")) return null;
  const projectId = query.get("project") || "";
  const issueId = query.get("issue") || "";
  const intent = query.get("intent");
  if (!uuid.test(projectId) || !uuid.test(issueId) || (intent !== "design" && intent !== "develop")) return "invalid";
  return { projectId, issueId, intent };
}

export function issueTaskPrompt(
  issue: DomainProjectIssue,
  intent: IssueTaskIntent,
  labels: { title: string; requirement: string; design: string },
): string {
  const sections = [labels.title];
  // Preserve Markdown and code fences verbatim: this is editable user input.
  if (issue.requirement_document?.trim()) sections.push(`${labels.requirement}\n\n${issue.requirement_document}`);
  if (intent === "develop" && issue.design_document?.trim()) sections.push(`${labels.design}\n\n${issue.design_document}`);
  return sections.join("\n\n");
}

export function newTaskDraftKey(userId: string, projectId = "", source?: Pick<IssueTaskSource, "issueId" | "intent">): string {
  const base = `jingjiaagent:new-task-draft:${userId}:${projectId}`;
  return source ? `${base}:issue:${source.issueId}:${source.intent}` : base;
}

export function parseTaskDraft<T extends { projectId?: string }>(serialized: string | null, projectId: string): T | null {
  try {
    const saved = JSON.parse(serialized || "null");
    if (!saved || typeof saved !== "object" || Array.isArray(saved)) return null;
    // A draft may restore input, but must never change the entry's project or
    // copy input from a different project left in this slot by older clients.
    if (saved.projectId !== undefined && saved.projectId !== projectId) return null;
    return saved as T;
  } catch {
    return null;
  }
}

export function initialTaskContent(saved: { content?: string } | null, initialContent: string): string {
  // An intentionally cleared draft also takes precedence over the original issue.
  return typeof saved?.content === "string" ? saved.content : initialContent;
}
