import { type DomainProject } from "@/api/Api"
import { useCommonData } from "@/components/console/data-provider"
import AutoReviewDialog from "@/components/console/project/auto-review-dialog"
import EditProjectEnvDialog from "@/components/console/project/edit-project-env"
import EditProjectImageDialog from "@/components/console/project/edit-project-image"
import EditProjectNameDialog from "@/components/console/project/edit-project-name"
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Item, ItemActions, ItemContent, ItemDescription, ItemMedia, ItemTitle } from "@/components/ui/item"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { isProjectRepoUnbound } from "@/utils/project"
import { AUTOMATIC_PR_REVIEW_ENABLED } from "@/utils/runtime-scope"
import { IconBrandGithub, IconDeviceImacCode, IconPencil, IconSettings, IconSparkles, IconTrash, IconViewfinder } from "@tabler/icons-react"
import { MoreVertical } from "lucide-react"
import { useState } from "react"
import { useNavigate } from "react-router-dom"
import { useTranslation } from "react-i18next"
import { useNavigationActions,projectTarget } from "@/components/console/work/navigation-actions"

interface ProjectInfoProps {
  project?: DomainProject
  onRefresh: () => void
}

const ProjectInfo = ({
  project,
  onRefresh,
}: ProjectInfoProps) => {
  const [isEditNameDialogOpen, setIsEditNameDialogOpen] = useState(false)
  const [editingProject, setEditingProject] = useState<DomainProject | undefined>(undefined)
  const [envDialogOpen, setEnvDialogOpen] = useState(false)
  const [imageDialogOpen, setImageDialogOpen] = useState(false)
  const [autoReviewDialogOpen, setAutoReviewDialogOpen] = useState(false)
  const navigate = useNavigate()
  const { t } = useTranslation()
  const navigationActions=useNavigationActions();
  const {user}=useCommonData();

  const handleEditProjectName = () => {
    if (!project) return
    setEditingProject(project)
    setIsEditNameDialogOpen(true)
  }

  const handleEditEnv = () => {
    if (!project) return
    setEditingProject(project)
    setEnvDialogOpen(true)
  }

  const handleEditImage = () => {
    if (!project) return
    setEditingProject(project)
    setImageDialogOpen(true)
  }

  const handleDeleteClick = () => {
    if (!project) return
    navigationActions.action(projectTarget(project,user.id),"delete")
  }

  const handleStartConversation = () => {
    if (!project || isRepoUnbound) return
    navigate(`/console/work?project=${encodeURIComponent(project.id || "")}`)
  }

  const handleAutoReview = () => {
    if (!project || !AUTOMATIC_PR_REVIEW_ENABLED) return
    setAutoReviewDialogOpen(true)
  }

  const isRepoUnbound = isProjectRepoUnbound(project)
  const autoReviewLabel = !AUTOMATIC_PR_REVIEW_ENABLED
    ? t("consoleProject.info.autoReviewDeferred")
    : project?.auto_review_enabled
    ? t("consoleProject.info.autoReviewEnabled")
    : t("consoleProject.info.autoReviewDisabled")

  return (
    <>
      <Item variant="outline" key={project?.id}>
        <ItemMedia>
          <Avatar>
            <AvatarImage src="" />
            <AvatarFallback>
              {project?.platform === "github"
                ? <IconBrandGithub className="size-5" />
                : (project?.name || " ").charAt(0).toUpperCase()}
            </AvatarFallback>
          </Avatar>
        </ItemMedia>
        <ItemContent>
          <ItemTitle className="font-bold">
            {project?.name || "-"}
          </ItemTitle>
          <ItemDescription className="flex flex-row gap-2 items-center">
            {isRepoUnbound && (
              <span className="text-warning">{t("consoleProject.info.unboundRepository")}</span>
            ) || (
              <span>{project?.description || t("consoleProject.info.noDescription")}</span>
            )}
          </ItemDescription>
        </ItemContent>
        <ItemActions>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant={project?.auto_review_enabled ? "secondary" : "ghost"}
                size="sm"
                disabled={isRepoUnbound || !AUTOMATIC_PR_REVIEW_ENABLED}
                onClick={handleAutoReview}
                className={`cursor-pointer disabled:cursor-not-allowed ${project?.auto_review_enabled ? "" : "text-muted-foreground"}`}
              >
                <IconViewfinder className="size-4" />
                {autoReviewLabel}
              </Button>
            </TooltipTrigger>
            <TooltipContent>
              {autoReviewLabel}
            </TooltipContent>
          </Tooltip>
          <Button
            variant="secondary"
            size="sm"
            disabled={isRepoUnbound}
            onClick={handleStartConversation}
            className="cursor-pointer disabled:cursor-not-allowed"
          >
            <IconSparkles className="size-4" />
            {t("assistantTasks.newTask")}
          </Button>
          
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon-sm">
                <MoreVertical className="size-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={handleEditProjectName}>
                <IconPencil />
                {t("consoleProject.info.editName")}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={handleEditEnv}>
                <IconSettings />
                {t("consoleProject.info.env")}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={handleEditImage}>
                <IconDeviceImacCode />
                {t("consoleProject.info.image")}
              </DropdownMenuItem>
              <DropdownMenuItem
                className="text-destructive"
                onClick={handleDeleteClick}
              >
                <IconTrash />
                {t("consoleProject.info.remove")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </ItemActions>
      </Item>

      <EditProjectNameDialog
        open={isEditNameDialogOpen}
        onOpenChange={setIsEditNameDialogOpen}
        project={editingProject}
        onSuccess={onRefresh}
      />

      <EditProjectEnvDialog
        open={envDialogOpen}
        onOpenChange={setEnvDialogOpen}
        project={editingProject}
        onSuccess={onRefresh}
      />

      <EditProjectImageDialog
        open={imageDialogOpen}
        onOpenChange={setImageDialogOpen}
        project={editingProject}
        onSuccess={onRefresh}
      />

      <AutoReviewDialog
        open={autoReviewDialogOpen}
        onOpenChange={setAutoReviewDialogOpen}
        project={project}
        onSuccess={onRefresh}
      />



    </>
  )
}

export default ProjectInfo
