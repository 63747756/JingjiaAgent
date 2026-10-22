import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useLocation } from "react-router-dom";
import {
  ArrowRight,
  CheckCircle2,
  Clock3,
  FolderPlus,
  Loader2,
  Plus,
  RefreshCw,
  Search,
  FileText,
  FolderOpen,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useCommonData } from "@/components/console/data-provider";
import { WorkComposer } from "@/components/console/work/work-composer";
import { WorkLibrary } from "@/components/console/work/work-library";
import { WorkArtifacts } from "@/components/console/work/work-artifacts";
import { WorkProjectSettings } from "@/components/console/work/work-project-settings";
import {
  WorkEmpty,
  WorkError,
  WorkLoading,
} from "@/components/console/work/work-common";
import {
  workError,
  workItems,
  workRequest,
  workTime,
  workTaskDisplay,
  type WorkProject,
  type WorkTask,
  type WorkTaskList,
} from "@/lib/work-api";
import { useTranslation } from "react-i18next";
import AddProjectDialog from "@/components/console/project/add-project";
import { taskLibraryURL } from "@/lib/assistant-tasks";
import ProjectOverviewInfoTab from "./project/overview/info-tab";
import ProjectOverviewIssuesTab from "./project/overview/issues-tab";
import ProjectInfo from "@/components/console/project/project-info";
import { getTaskDisplayName } from "@/utils/common";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { NavigationItemActions, projectTarget, taskTarget } from "@/components/console/work/navigation-actions";

function TaskCards({
  tasks,
  compact = false,
}: {
  tasks: WorkTask[];
  compact?: boolean;
}) {
  return (
    <div
      className={
        compact ? "space-y-2" : "grid gap-3 sm:grid-cols-2 xl:grid-cols-3"
      }
    >
      {tasks.map((task) => {
        const display = workTaskDisplay(task);
        return (
          <NavigationItemActions key={task.id} target={taskTarget(task)}>
          <Link
            to={`/console/task/${task.id}`}
            className="group flex min-w-0 items-start gap-3 rounded-xl border bg-card p-4 pr-10 transition-colors hover:border-primary/30 hover:bg-accent/30"
          >
            <div className="rounded-lg bg-accent p-2 text-primary">
              {display.completed ? (
                <CheckCircle2 className="size-4" />
              ) : (
                <Clock3 className="size-4" />
              )}
            </div>
            <div className="min-w-0 flex-1">
              <h3 className="truncate text-sm font-medium">
                {getTaskDisplayName(task)}
              </h3>
              <div className="mt-2 flex items-center justify-between gap-2 text-xs text-muted-foreground">
                <span>{display.label}</span>
                <span>{workTime(task.created_at)}</span>
              </div>
            </div>
            <ArrowRight className="mt-2 size-3.5 text-muted-foreground/40 group-hover:text-primary" />
          </Link>
          </NavigationItemActions>
        );
      })}
    </div>
  );
}

export default function WorkHomePage() {
  const { t } = useTranslation();
  const location = useLocation();
  return (
    <div className="mx-auto h-full min-h-0 w-full max-w-5xl overflow-hidden px-1 sm:px-6">
      <div className="mx-auto flex w-full max-w-[800px] flex-col pt-[clamp(1rem,21vh,12rem)] [@media(max-height:600px)]:pt-4">
        <div className="mb-10 shrink-0 text-center sm:mb-12">
          <h1 className="text-2xl font-semibold tracking-tight sm:text-[32px] sm:leading-10">
            {t("assistantTasks.homeTitle")}
          </h1>
        </div>
        <WorkComposer key={location.search} />
      </div>
    </div>
  );
}

export function WorkTasksPage({
  projectId,
  allowCreate = true,
}: {
  projectId?: string;
  allowCreate?: boolean;
}) {
  const { t } = useTranslation();
  const location = useLocation();
  const unlinked =
    !projectId && new URLSearchParams(location.search).get("unlinked") === "1";
  const [tasks, setTasks] = useState<WorkTask[]>([]);
  const [page, setPage] = useState(1);
  const [more, setMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [search, setSearch] = useState("");
  const generation = useRef(0);
  const loadedPages=useRef(1);
  const load = useCallback(
    async (nextPage?: number) => {
      const request = ++generation.current;
      setLoading(true);
      setError("");
      try {
        const targetPage=nextPage??loadedPages.current;
        const data=await Promise.all(Array.from({length:nextPage?1:targetPage},(_,i)=>workRequest<WorkTaskList>(taskLibraryURL({page:nextPage??i+1,limit:24,projectId,unlinked}))));
        if (generation.current === request) {
          const result = data.flatMap(workItems);
          setTasks((prev) => [...(nextPage && nextPage>1?prev:[]),...result].filter((task,index,all)=>all.findIndex(row=>row.id===task.id)===index));
          setPage(targetPage);loadedPages.current=targetPage;
          setMore(!!data.at(-1)?.has_more);
        }
      } catch (e) {
        if (generation.current === request) setError(workError(e));
      } finally {
        if (generation.current === request) setLoading(false);
      }
    },
    [projectId, unlinked],
  );
  useEffect(() => {
    loadedPages.current=1;
    void load();
    return () => {
      generation.current++;
    };
  }, [load]);
  useEffect(()=>{const refresh=()=>void load();window.addEventListener("task-library-changed",refresh);return()=>window.removeEventListener("task-library-changed",refresh);},[load]);
  useEffect(() => {
    const timer = setInterval(() => {
      if (page === 1) void load();
    }, 15000);
    return () => clearInterval(timer);
  }, [load, page]);
  const visible = tasks.filter((task) =>
    getTaskDisplayName(task).toLowerCase().includes(search.toLowerCase()),
  );
  return (
    <section className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">{t("assistantTasks.tasks")}</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {t("assistantTasks.taskPrivacy")}
          </p>
        </div>
        {allowCreate && (
          <Button size="sm" asChild>
            <Link
              to={
                projectId
                  ? `/console/work?project=${encodeURIComponent(projectId)}`
                  : "/console/work"
              }
            >
              <Plus className="size-4" />
              {t("assistantTasks.newTask")}
            </Link>
          </Button>
        )}
      </div>
      <div className="flex items-center gap-2">
        <div className="relative max-w-sm flex-1">
          <Search className="absolute left-3 top-2.5 size-4 text-muted-foreground" />
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t("assistantTasks.searchLoadedTasks")}
            className="pl-9"
            aria-label={t("assistantTasks.searchTasks")}
          />
        </div>
        {!projectId && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => void load()}
            disabled={loading}
          >
            <RefreshCw className="size-4" />
          </Button>
        )}
      </div>
      {error && <WorkError message={error} retry={() => void load()} />}
      {loading && !tasks.length ? (
        <WorkLoading />
      ) : !visible.length && !error ? (
        <WorkEmpty
          title={
            search
              ? t("assistantTasks.noMatchingTasks")
              : t("assistantTasks.emptyTasks")
          }
          description={t("assistantTasks.firstTaskHint")}
        />
      ) : (
        <TaskCards tasks={visible} />
      )}
      {more && (
        <div className="text-center">
          <Button
            variant="outline"
            disabled={loading}
            onClick={() => void load(page + 1)}
          >
            {loading && <Loader2 className="size-4 animate-spin" />}
            {t("assistantTasks.loadMore")}
          </Button>
        </div>
      )}
    </section>
  );
}

export function WorkProjectsPage() {
  const { t } = useTranslation();
  const { projects, loadingProjects, reloadProjects,user } = useCommonData();
  const [open, setOpen] = useState(false);
  return (
    <section className="space-y-6">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">
            {t("assistantTasks.projects")}
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {t("assistantTasks.projectsDescription")}
          </p>
        </div>
        <Button size="sm" onClick={() => setOpen(true)}>
          <FolderPlus className="size-4" />
          {t("assistantTasks.newProject")}
        </Button>
      </div>
      {loadingProjects ? (
        <WorkLoading />
      ) : !projects.length ? (
        <WorkEmpty
          title={t("assistantTasks.emptyProjects")}
          description={t("assistantTasks.createProjectHint")}
        />
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {(projects as WorkProject[]).map((project) => (
            <NavigationItemActions key={project.id} target={projectTarget(project,user.id)}>
            <Link
              to={`/console/project/${project.id}`}
              className="block space-y-3 rounded-xl border bg-card p-5 pr-10 transition-colors hover:border-primary/30"
            >
              <FolderOpen className="size-6 text-primary" />
              <h2 className="truncate font-medium">{project.name}</h2>
              <p className="line-clamp-2 text-sm text-muted-foreground">
                {project.description ||
                  project.rules ||
                  t("assistantTasks.projectsDescription")}
              </p>
              {project.repo_url && (
                <p className="truncate text-xs text-muted-foreground">
                  {project.repo_url}
                </p>
              )}
            </Link>
            </NavigationItemActions>
          ))}
        </div>
      )}
      <AddProjectDialog
        open={open}
        onOpenChange={setOpen}
        onSuccess={reloadProjects}
      />
    </section>
  );
}

export function BusinessProjectOverview({
  project,
  onRefresh,
}: {
  project: WorkProject;
  onRefresh?: () => void;
}) {
  const { t } = useTranslation();
  const { user } = useCommonData();
  const canWrite =
    project.user?.id === user.id ||
    project.collaborators?.some(
      (member) => member.id === user.id && member.permission === "read_write",
    );
  const location = useLocation();
  const [tab, setTab] = useState("tasks");
  useEffect(() => {
    const requested = new URLSearchParams(location.search).get("tab");
    setTab(requested === "settings" && canWrite ? "settings" : "tasks");
  }, [location.search, canWrite]);
  useEffect(()=>{const refresh=(event:Event)=>{const change=(event as CustomEvent).detail;if(change?.kind==="project"&&change.id===project.id&&change.action==="rename")onRefresh?.();};window.addEventListener("task-library-changed",refresh);return()=>window.removeEventListener("task-library-changed",refresh);},[project.id,onRefresh]);
  return (
    <div className={tab === "artifacts" ? "flex h-full min-h-0 flex-col gap-5" : "space-y-5"}>
      <header className="flex shrink-0 items-start gap-3">
        <div className="rounded-xl bg-accent p-3 text-primary">
          <FolderOpen className="size-6" />
        </div>
        <div className="min-w-0 flex-1">
          <h1 className="text-xl font-semibold">{project.name}</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {project.description || t("assistantTasks.projectPrivacy")}
          </p>
        </div>
      </header>
      {project.rules && (
        <div className={`rounded-xl border bg-card p-4 ${tab === "artifacts" ? "max-h-36 shrink-0 overflow-auto" : ""}`}>
          <h2 className="mb-2 flex items-center gap-2 text-xs font-medium">
            <FileText className="size-3.5" />
            {t("assistantTasks.projectRules")}
          </h2>
          <p className="whitespace-pre-wrap text-sm leading-6 text-muted-foreground">
            {project.rules}
          </p>
        </div>
      )}
      <Tabs value={tab} onValueChange={setTab} className={tab === "artifacts" ? "min-h-0 flex-1" : undefined}>
        <TabsList className="shrink-0">
          <TabsTrigger value="tasks">{t("assistantTasks.tasks")}</TabsTrigger>
          {project.repo_url && (
            <TabsTrigger value="repository">
              {t("assistantTasks.repository")}
            </TabsTrigger>
          )}
          {project.repo_url && (
            <TabsTrigger value="issues">
              {t("assistantTasks.issues")}
            </TabsTrigger>
          )}
          <TabsTrigger value="resources">
            {t("assistantTasks.resources")}
          </TabsTrigger>
          <TabsTrigger value="artifacts">
            {t("assistantTasks.artifacts")}
          </TabsTrigger>
          {canWrite && (
            <TabsTrigger value="settings">
              {t("assistantTasks.settings")}
            </TabsTrigger>
          )}
        </TabsList>
        <TabsContent value="tasks" className="mt-5">
          <WorkTasksPage projectId={project.id} allowCreate={!!canWrite} />
        </TabsContent>
        {project.repo_url && (
          <TabsContent value="repository" className="mt-5 space-y-4">
            <ProjectInfo project={project} onRefresh={() => onRefresh?.()} />
            <ProjectOverviewInfoTab
              projectId={project.id || ""}
              project={project}
            />
          </TabsContent>
        )}
        {project.repo_url && (
          <TabsContent value="issues" className="mt-5">
            <ProjectOverviewIssuesTab
              projectId={project.id || ""}
              project={project}
              onTaskCreated={onRefresh}
            />
          </TabsContent>
        )}
        <TabsContent value="resources" className="mt-5">
          {project.id && <WorkLibrary projectId={project.id} canWrite={!!canWrite} />}
        </TabsContent>
        <TabsContent value="artifacts" className="mt-5 min-h-0 overflow-hidden">
          <WorkArtifacts projectId={project.id} />
        </TabsContent>
        {canWrite && (
          <TabsContent value="settings" className="mt-5">
            <WorkProjectSettings
              key={project.updated_at}
              project={project}
              onSaved={onRefresh}
            />
          </TabsContent>
        )}
      </Tabs>
    </div>
  );
}

export function WorkArtifactsPage() {
  return <WorkArtifacts />;
}
