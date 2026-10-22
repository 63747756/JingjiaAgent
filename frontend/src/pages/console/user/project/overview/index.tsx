import { useCallback, useEffect, useRef, useState } from "react"
import { useParams } from "react-router-dom"
import { type DomainProject } from "@/api/Api"
import { apiRequest } from "@/utils/requestUtils"
import { toast } from "sonner"
import { useTranslation } from "react-i18next"
import { BusinessProjectOverview } from "@/pages/console/user/work"
import { Spinner } from "@/components/ui/spinner";
import type { WorkProject } from "@/lib/work-api"

export default function ProjectOverviewPage() {
  const { t } = useTranslation()
  const { projectId = "" } = useParams<{ projectId: string }>()
  const projectIdRef = useRef(projectId)

  const [project, setProject] = useState<DomainProject | undefined>(undefined)

  useEffect(() => {
    projectIdRef.current = projectId
  }, [projectId])

  const fetchProject = useCallback(async () => {
    const requestedId = projectId
    await apiRequest("v1UsersProjectsDetail", {}, [requestedId], (resp) => {
      if (projectIdRef.current !== requestedId) return
      if (resp.code === 0) {
        setProject(resp.data)
      } else {
        toast.error(resp.message || t("projectOverview.toast.fetchProjectFailed"))
      }
    })
  }, [projectId, t])

  useEffect(() => {
    if (!projectId) {
      return
    }

    let active = true
    queueMicrotask(() => {
      if (!active) {
        return
      }
      setProject(undefined)
      fetchProject()
    })

    return () => {
      active = false
    }
  }, [fetchProject, projectId])

  if (!project) return <div className="py-8"><Spinner /></div>;
  return <BusinessProjectOverview key={project.id} project={project as WorkProject} onRefresh={fetchProject} />;
}
