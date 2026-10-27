import { useEffect, useState } from "react";
import { useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import type { DomainProjectIssue } from "@/api/Api";
import { issueTaskPrompt, parseIssueTaskSource, type IssueTaskSource } from "@/lib/issue-task-entry";
import { workError, workRequest } from "@/lib/work-api";
import { WorkComposer } from "./work-composer";
import { WorkError, WorkLoading } from "./work-common";

function IssueTaskComposer({ source }: { source: IssueTaskSource }) {
  const { t } = useTranslation();
  const [issue, setIssue] = useState<DomainProjectIssue | null>(null);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setIssue(null);
    setError("");
    const query = new URLSearchParams({ project_id: source.projectId, issue_id: source.issueId });
    void workRequest<DomainProjectIssue>(`/api/v1/users/work/issue-context?${query}`, { signal: controller.signal })
      .then(value => {
        if (!controller.signal.aborted) {
          if (value.id !== source.issueId) throw new Error(t("assistantTasks.issueUnavailable"));
          setIssue(value);
        }
      })
      .catch(error => { if (!controller.signal.aborted) setError(workError(error)); });
    return () => controller.abort();
  }, [source.projectId, source.issueId, attempt, t]);

  if (error) return <WorkError message={error} retry={() => setAttempt(value => value + 1)} />;
  if (!issue) return <WorkLoading text={t("assistantTasks.loadingIssue")} />;
  const title = issue.title || t("consoleProject.issue.untitled");
  const initialContent = issueTaskPrompt(issue, source.intent, {
    title: t(`consoleProject.issueTask.${source.intent}.promptTitle`, { title }),
    requirement: t(`consoleProject.issueTask.${source.intent}.requirementSection`),
    design: t("consoleProject.issueTask.develop.designSection"),
  });
  return <WorkComposer projectId={source.projectId} issueSource={{ ...source, title }} initialContent={initialContent} />;
}

export function WorkEntryComposer() {
  const { search } = useLocation();
  const { t } = useTranslation();
  const source = parseIssueTaskSource(search);
  if (source === "invalid") return <WorkError message={t("assistantTasks.issueUnavailable")} />;
  return source ? <IssueTaskComposer key={search} source={source} /> : <WorkComposer key={search} />;
}
