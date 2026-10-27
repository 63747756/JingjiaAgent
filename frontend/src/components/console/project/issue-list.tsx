import { type DomainProjectIssue } from "@/api/Api";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { CalendarDays } from "lucide-react";
import IssueCard from "./issue-card";
import { useTranslation } from "react-i18next";

export default function ProjectIssueList({ issues, projectId, onViewIssue, onIssueDeleted }: { issues: DomainProjectIssue[], projectId: string, onViewIssue: (issue: DomainProjectIssue) => void, onIssueDeleted?: () => void }) {
  const { t } = useTranslation();

  if (issues.length === 0) {
    return (
      <div className="flex-1 min-h-0 flex flex-col">
        <Empty className="border flex-1">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <CalendarDays />
            </EmptyMedia>
            <EmptyTitle>{t("consoleProject.issue.emptyTitle")}</EmptyTitle>
            <EmptyDescription>
              {t("consoleProject.issue.emptyDescription")}
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      </div>
    )
  }
  
  return (
    <div className="flex-1 rounded-md flex flex-col gap-3">
      {issues.map((issue) => (
        <IssueCard 
          key={issue.id} 
          issue={issue} 
          projectId={projectId}
          onViewIssue={onViewIssue}
          onIssueDeleted={onIssueDeleted}
        />
      ))}
    </div>
  )
}
