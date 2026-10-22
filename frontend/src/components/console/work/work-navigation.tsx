import { Link, useLocation } from "react-router-dom";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  Plus,
  FileCheck2,
  Package,
  ChevronRight,
  Minimize2,
  Maximize2,
  Bot,
  Loader2,
  Pin,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import {
  SidebarGroup,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuAction,
  SidebarMenuSub,
  SidebarMenuSubItem,
  SidebarMenuSubButton,
} from "@/components/ui/sidebar";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { Button } from "@/components/ui/button";
import { useCommonData } from "@/components/console/data-provider";
import { getTaskDisplayName } from "@/utils/common";
import {
  workItems,
  workRequest,
  type WorkTask,
  type WorkTaskList,
  type WorkProject,
} from "@/lib/work-api";
import { taskLibraryURL } from "@/lib/assistant-tasks";
import AddProjectDialog from "@/components/console/project/add-project";
import { cn } from "@/lib/utils";
import {
  NavigationItemActions,
  projectTarget,
  taskTarget,
  useNavigationActions,
} from "./navigation-actions";

const sectionActionClassName =
  "size-7 text-muted-foreground aria-expanded:not-hover:bg-transparent aria-expanded:not-hover:text-muted-foreground";

type LocatedTask = WorkTask & { project_id: string };
function useExpanded(key: string, initial: boolean) {
  const [open, setOpen] = useState(initial);
  useEffect(() => {
    try {
      const saved = localStorage.getItem(key);
      setOpen(saved === null ? initial : saved === "true");
    } catch {
      setOpen(initial);
    }
  }, [key, initial]);
  const change = useCallback(
    (next: boolean) => {
      setOpen(next);
      try {
        localStorage.setItem(key, String(next));
      } catch {
        /* Navigation remains usable without storage. */
      }
    },
    [key],
  );
  return [open, change] as const;
}

function useTaskLibrary(active: boolean, projectId?: string) {
  const [tasks, setTasks] = useState<WorkTask[]>([]);
  const pages = useRef(1);
  const [more, setMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(false);
  const generation = useRef(0);
  const load = useCallback(
    async (append = false) => {
      const request = ++generation.current;
      setLoading(true);
      setError(false);
      try {
        const nextPage = append ? pages.current + 1 : pages.current;
        const results = await Promise.all(
          Array.from({ length: append ? 1 : nextPage }, (_, index) =>
            workRequest<WorkTaskList>(
              taskLibraryURL({
                projectId,
                unlinked: !projectId,
                page: append ? nextPage : index + 1,
                limit: 10,
              }),
            ),
          ),
        );
        const loaded = results.flatMap(workItems);
        if (request === generation.current) {
          setTasks((previous) =>
            [...(append ? previous : []), ...loaded].filter(
              (task, index, all) =>
                all.findIndex((item) => item.id === task.id) === index,
            ),
          );
          pages.current = nextPage;
          setMore(Boolean(results.at(-1)?.has_more));
        }
      } catch {
        if (request === generation.current) setError(true);
      } finally {
        if (request === generation.current) setLoading(false);
      }
    },
    [projectId],
  );
  useEffect(() => {
    if (!active) return;
    void load();
    const refresh = () => {
      if (!document.hidden) void load();
    };
    const timer = window.setInterval(refresh, 30000);
    window.addEventListener("task-library-changed", refresh);
    return () => {
      generation.current++;
      clearInterval(timer);
      window.removeEventListener("task-library-changed", refresh);
    };
  }, [active, load]);
  return {
    tasks,
    more,
    loading,
    error,
    reload: () => void load(),
    loadMore: () => void load(true),
  };
}

function SidebarTasks({
  active,
  projectId,
  current,
}: {
  active: boolean;
  projectId?: string;
  current?: LocatedTask | null;
}) {
  const { t } = useTranslation();
  const location = useLocation();
  const { pins } = useNavigationActions();
  const { tasks, more, loading, error, reload, loadMore } = useTaskLibrary(
    active,
    projectId,
  );
  return (
    <SidebarMenuSub className="mr-0 translate-x-0 pr-0">
      {[
        ...tasks,
        ...(current &&
        (current.project_id || "") === (projectId || "") &&
        !tasks.some((task) => task.id === current.id)
          ? [current]
          : []),
      ].map((task) => (
        <SidebarMenuSubItem key={task.id}>
          <NavigationItemActions
            target={taskTarget(task, projectId)}
            className="size-7 shrink-0 opacity-100 md:opacity-0 md:group-hover/nav-row:opacity-100 md:group-focus-within/nav-row:opacity-100 data-[state=open]:opacity-100"
          >
            <SidebarMenuSubButton
              asChild
              className="h-8 translate-x-0 pr-9"
              isActive={location.pathname === `/console/task/${task.id}`}
            >
              <Link
                to={`/console/task/${task.id}`}
                title={getTaskDisplayName(task)}
              >
                {pins[`task:${task.id}`] && (
                  <Pin className="size-3 shrink-0 text-muted-foreground" />
                )}
                <span className="truncate">{getTaskDisplayName(task)}</span>
                {task.status === "processing" && (
                  <span className="ml-auto size-1.5 shrink-0 rounded-full bg-primary" />
                )}
              </Link>
            </SidebarMenuSubButton>
          </NavigationItemActions>
        </SidebarMenuSubItem>
      ))}
      {loading && !tasks.length && (
        <li className="flex items-center gap-2 px-2 py-2 text-xs text-muted-foreground">
          <Loader2 className="size-3 animate-spin" />
          {t("assistantTasks.loading")}
        </li>
      )}
      {error && (
        <li>
          <Button
            variant="ghost"
            size="sm"
            onClick={reload}
            className="h-auto px-2 py-1 text-xs"
          >
            {t("assistantTasks.retryTasks")}
          </Button>
        </li>
      )}
      {!loading && !error && !tasks.length && (
        <li className="px-2 py-2 text-xs text-muted-foreground">
          {t("assistantTasks.emptyTasks")}
        </li>
      )}
      {more && (
        <li>
          <Button
            variant="ghost"
            size="sm"
            disabled={loading}
            onClick={loadMore}
            className="h-auto px-2 py-1 text-xs"
          >
            {t("assistantTasks.loadMore")}
          </Button>
        </li>
      )}
    </SidebarMenuSub>
  );
}

function ProjectNode({
  project,
  current,
}: {
  project: WorkProject;
  current?: LocatedTask | null;
}) {
  const location = useLocation();
  const { t } = useTranslation();
  const { user } = useCommonData();
  const { pins, action } = useNavigationActions();
  const target = projectTarget(project, user.id);
  const [open, setOpen] = useExpanded(
    `navigation:${user.id}:project:${project.id}`,
    false,
  );
  useEffect(() => {
    if (
      location.pathname === `/console/project/${project.id}` ||
      current?.project_id === project.id
    )
      setOpen(true);
  }, [location.pathname, project.id, current?.project_id, setOpen]);
  useEffect(() => {
    const reveal = (event: Event) => {
      if (
        (event as CustomEvent).detail?.projectId === project.id &&
        !(event as CustomEvent).detail?.action
      )
        setOpen(true);
    };
    window.addEventListener("task-library-changed", reveal);
    return () => window.removeEventListener("task-library-changed", reveal);
  }, [project.id, setOpen]);
  return (
    <Collapsible open={open} onOpenChange={setOpen} asChild>
      <SidebarMenuItem>
        <NavigationItemActions
          target={target}
          className="size-7 shrink-0 opacity-100 md:opacity-0 md:group-hover/nav-row:opacity-100 md:group-focus-within/nav-row:opacity-100 data-[state=open]:opacity-100"
          trailingAction={
            target.canWrite && (
              <Button
                variant="ghost"
                size="icon"
                className="size-7 opacity-100 md:opacity-0 md:group-hover/nav-row:opacity-100 md:group-focus-within/nav-row:opacity-100"
                aria-label={t("navigationActions.newTaskIn", {
                  name: project.name,
                })}
                onClick={() => action(target, "new")}
              >
                <Plus className="size-3.5" />
              </Button>
            )
          }
        >
          <SidebarMenuButton
            asChild
            isActive={location.pathname === `/console/project/${project.id}`}
            tooltip={project.name}
            className="pl-8 pr-16 group-has-data-[sidebar=menu-action]/menu-item:pr-16"
          >
            <Link to={`/console/project/${project.id}`}>
              {pins[`project:${project.id}`] && (
                <Pin className="size-3 shrink-0 text-muted-foreground" />
              )}
              <span className="truncate">{project.name}</span>
            </Link>
          </SidebarMenuButton>
          <CollapsibleTrigger asChild>
            <SidebarMenuAction
              className="left-1 right-auto"
              aria-label={t("assistantTasks.toggleProject", {
                name: project.name,
              })}
            >
              <ChevronRight
                className={cn("transition-transform", open && "rotate-90")}
              />
            </SidebarMenuAction>
          </CollapsibleTrigger>
        </NavigationItemActions>
        <CollapsibleContent forceMount hidden={!open}>
          <SidebarTasks
            active={open}
            projectId={project.id}
            current={current}
          />
        </CollapsibleContent>
      </SidebarMenuItem>
    </Collapsible>
  );
}

export function WorkNavigation() {
  const location = useLocation();
  const { t } = useTranslation();
  const { projects, reloadProjects, user } = useCommonData();
  const { pins } = useNavigationActions();
  const [projectsOpen, setProjectsOpen] = useExpanded(
    `navigation:${user.id}:projects`,
    true,
  );
  const [tasksOpen, setTasksOpen] = useExpanded(
    `navigation:${user.id}:tasks`,
    true,
  );
  const [current, setCurrent] = useState<LocatedTask | null>(null);
  useEffect(() => {
    const id = location.pathname.match(/^\/console\/task\/([^/]+)$/)?.[1];
    let active = true;
    if (!id) {
      setCurrent(null);
      return;
    }
    const load = () =>
      workRequest<LocatedTask>(`/api/v1/users/work/navigation/tasks/${id}`)
        .then((task) => {
          if (active) {
            setCurrent(task);
            if (task.project_id) setProjectsOpen(true);
            else setTasksOpen(true);
          }
        })
        .catch(() => {
          if (active) setCurrent(null);
        });
    void load();
    window.addEventListener("task-library-changed", load);
    return () => {
      active = false;
      window.removeEventListener("task-library-changed", load);
    };
  }, [location.pathname, setProjectsOpen, setTasksOpen]);
  const [createOpen, setCreateOpen] = useState(false);
  useEffect(() => {
    const reveal = (event: Event) => {
      if ((event as CustomEvent).detail?.action) return;
      if ((event as CustomEvent).detail?.projectId) setProjectsOpen(true);
      else setTasksOpen(true);
    };
    window.addEventListener("task-library-changed", reveal);
    return () => window.removeEventListener("task-library-changed", reveal);
  }, [setProjectsOpen, setTasksOpen]);
  const links = [
    { to: "/console/work", label: t("assistantTasks.newTask"), icon: Bot },
    {
      to: "/console/artifacts",
      label: t("consoleShell.breadcrumbs.artifacts"),
      icon: FileCheck2,
    },
    {
      to: "/console/skills",
      label: t("skillsExperience.packages"),
      icon: Package,
    },
  ];
  return (
    <>
      <SidebarGroup className="mt-3 px-0">
        <SidebarMenu>
          {links.map((item) => (
            <SidebarMenuItem key={item.to}>
              <SidebarMenuButton
                asChild
                isActive={
                  location.pathname === item.to ||
                  (item.to === "/console/skills" &&
                    location.pathname === "/console/skill-market")
                }
                tooltip={item.label}
              >
                <Link to={item.to}>
                  <item.icon className="size-4" />
                  <span>{item.label}</span>
                </Link>
              </SidebarMenuButton>
            </SidebarMenuItem>
          ))}
        </SidebarMenu>
      </SidebarGroup>
      <SidebarGroup className="mt-5 px-0">
        <Collapsible open={projectsOpen} onOpenChange={setProjectsOpen}>
          <div className="mb-1 flex h-8 items-center gap-1 pl-2 pr-1 group-data-[collapsible=icon]:hidden">
            <Link
              to="/console/projects"
              className="flex-1 text-xs font-medium text-muted-foreground hover:text-foreground"
            >
              {t("assistantTasks.projects")}
            </Link>
            <CollapsibleTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                className={sectionActionClassName}
                aria-label={t("assistantTasks.toggleProjects")}
              >
                {projectsOpen ? (
                  <Minimize2 className="size-3.5" />
                ) : (
                  <Maximize2 className="size-3.5" />
                )}
              </Button>
            </CollapsibleTrigger>
            <Button
              variant="ghost"
              size="icon"
              className={sectionActionClassName}
              onClick={() => setCreateOpen(true)}
              aria-label={t("assistantTasks.newProject")}
            >
              <Plus className="size-3.5" />
            </Button>
          </div>
          <CollapsibleContent forceMount hidden={!projectsOpen}>
            <SidebarMenu>
              {[...(projects as WorkProject[])]
                .sort((a, b) =>
                  (pins[`project:${b.id}`] || "").localeCompare(
                    pins[`project:${a.id}`] || "",
                  ),
                )
                .map((project) => (
                  <ProjectNode
                    key={project.id}
                    project={project}
                    current={current}
                  />
                ))}
              {!projects.length && (
                <li className="px-3 py-2 text-xs text-muted-foreground group-data-[collapsible=icon]:hidden">
                  {t("assistantTasks.emptyProjects")}
                </li>
              )}
            </SidebarMenu>
          </CollapsibleContent>
        </Collapsible>
      </SidebarGroup>
      <SidebarGroup className="px-0">
        <Collapsible open={tasksOpen} onOpenChange={setTasksOpen}>
          <div className="mb-1 flex h-8 items-center gap-1 pl-2 pr-1 group-data-[collapsible=icon]:hidden">
            <Link
              to="/console/tasks?unlinked=1"
              className="flex-1 text-xs font-medium text-muted-foreground hover:text-foreground"
            >
              {t("assistantTasks.tasks")}
            </Link>
            <CollapsibleTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                className={sectionActionClassName}
                aria-label={t("assistantTasks.toggleTasks")}
              >
                {tasksOpen ? (
                  <Minimize2 className="size-3.5" />
                ) : (
                  <Maximize2 className="size-3.5" />
                )}
              </Button>
            </CollapsibleTrigger>
            <Button
              asChild
              variant="ghost"
              size="icon"
              className={sectionActionClassName}
            >
              <Link to="/console/work" aria-label={t("assistantTasks.newTask")}>
                <Plus className="size-3.5" />
              </Link>
            </Button>
          </div>
          <CollapsibleContent forceMount hidden={!tasksOpen}>
            <SidebarTasks active={tasksOpen} current={current} />
          </CollapsibleContent>
        </Collapsible>
      </SidebarGroup>
      <AddProjectDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onSuccess={reloadProjects}
      />
    </>
  );
}
