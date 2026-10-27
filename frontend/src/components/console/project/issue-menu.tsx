import { type DomainProjectIssue } from "@/api/Api"
import { Button } from "@/components/ui/button"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Spinner } from "@/components/ui/spinner"
import { apiRequest } from "@/utils/requestUtils"
import { IconDeviceImacCode, IconSparkles, IconTrash } from "@tabler/icons-react"
import { MoreVertical } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"
import { useTranslation } from "react-i18next"
import { useNavigate } from "react-router-dom"
import { issueTaskURL } from "@/lib/issue-task-entry"

interface IssueMenuProps {
  issue?: DomainProjectIssue
  projectId: string
  onIssueDeleted?: () => void
}

export default function IssueMenu({ issue, projectId, onIssueDeleted }: IssueMenuProps) {
  const navigate = useNavigate()
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const { t } = useTranslation()

  if (!issue) {
    return null
  }

  const handleDeleteIssue = () => {
    if (!issue.id) {
      toast.error(t("consoleProject.issue.delete.toast.missingId"))
      setDeleteDialogOpen(false)
      return
    }

    setDeleting(true)
    apiRequest(
      "v1UsersProjectsIssuesDelete",
      {},
      [projectId, issue.id],
      (resp) => {
        setDeleting(false)
        setDeleteDialogOpen(false)
        if (resp.code === 0) {
          toast.success(t("consoleProject.issue.delete.toast.deleted"))
          onIssueDeleted?.()
        } else {
          toast.error(resp.message || t("consoleProject.issue.delete.toast.deleteFailed"))
        }
      },
      () => {
        setDeleting(false)
        setDeleteDialogOpen(false)
        toast.error(t("consoleProject.issue.delete.toast.deleteFailed"))
      }
    )
  }

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon-sm">
            <MoreVertical className="size-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="min-w-36">
          <DropdownMenuItem disabled={!issue.id} onClick={() => navigate(issueTaskURL(projectId, issue.id!, "design"))}>
            <IconSparkles />
            {t("consoleProject.issue.actions.startDesign")}
          </DropdownMenuItem>
          <DropdownMenuItem disabled={!issue.id} onClick={() => navigate(issueTaskURL(projectId, issue.id!, "develop"))}>
            <IconDeviceImacCode />
            {t("consoleProject.issue.actions.startDevelop")}
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => setDeleteDialogOpen(true)}>
            <IconTrash />
            {t("consoleProject.issue.actions.delete")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <AlertDialog open={deleteDialogOpen} onOpenChange={(open) => !deleting && setDeleteDialogOpen(open)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("consoleProject.issue.delete.title")}</AlertDialogTitle>
          </AlertDialogHeader>
          <AlertDialogDescription>
            {t("consoleProject.issue.delete.description", { title: issue.title || t("consoleProject.issue.untitled") })}
          </AlertDialogDescription>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>{t("consoleProject.common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={(event) => {
                event.preventDefault()
                handleDeleteIssue()
              }}
              disabled={deleting}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {deleting ? (
                <>
                  <Spinner />
                  {t("consoleProject.issue.delete.deleting")}
                </>
              ) : t("consoleProject.issue.actions.delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
