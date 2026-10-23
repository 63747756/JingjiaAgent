import {
  createContext,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  MoreHorizontal,
  MoreVertical,
  Pin,
  PinOff,
  Pencil,
  Plus,
  Settings2,
  Trash2,
  Loader2,
  Square,
} from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import {
  ContextMenu,
  ContextMenuTrigger,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
} from "@/components/ui/context-menu";
import { useCommonData } from "@/components/console/data-provider";
import {
  workRequest,
  workError,
  type WorkProject,
  type WorkTask,
} from "@/lib/work-api";
import { getTaskDisplayName } from "@/utils/common";

type Target = {
  kind: "task" | "project";
  id: string;
  name: string;
  status?: string;
  projectId?: string;
  canWrite?: boolean;
  canDelete?: boolean;
};
type PinRow = { kind: string; target_id: string; pinned_at: string };
type Change = {
  kind: string;
  id: string;
  action: string;
  title?: string;
  projectId?: string;
};
export function notifyNavigation(change: Change) {
  window.dispatchEvent(
    new CustomEvent("task-library-changed", { detail: change }),
  );
}
const NavigationContext = createContext<{
  pins: Record<string, string>;
  ready: boolean;
  busy: string;
  stoppingTaskId: string | null;
  action: (target: Target, action: string) => void;
}>({ pins: {}, ready: false, busy: "", stoppingTaskId: null, action: () => {} });
export const useNavigationActions = () => useContext(NavigationContext);

export function NavigationActionsProvider({
  children,
}: {
  children: ReactNode;
}) {
  const { t } = useTranslation();
  const { reloadProjects } = useCommonData();
  const location = useLocation(),
    navigate = useNavigate();
  const [pins, setPins] = useState<Record<string, string>>({});
  const [ready, setReady] = useState(false);
  const [busy, setBusy] = useState("");
  const [dialog, setDialog] = useState<{
    target: Target;
    action: string;
  } | null>(null);
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    const load = () =>
      workRequest<{ items: PinRow[] }>("/api/v1/users/work/navigation/pins")
        .then((data) => {
          if (active) {
            setPins(
              Object.fromEntries(
                data.items.map((p) => [
                  `${p.kind}:${p.target_id}`,
                  p.pinned_at,
                ]),
              ),
            );
            setReady(true);
          }
        })
        .catch(() => {
          if (active) setReady(false);
        });
    void load();
    window.addEventListener("focus", load);
    return () => {
      active = false;
      window.removeEventListener("focus", load);
    };
  }, []);
  const action = async (target: Target, operation: string) => {
    if (busy) return;
    if (operation === "new") {
      navigate(`/console/work?project=${target.id}`);
      return;
    }
    if (operation === "settings") {
      navigate(`/console/project/${target.id}?tab=settings`);
      return;
    }
    if (operation !== "pin") {
      let current = target;
      if (operation === "delete" && target.kind === "task") {
        setBusy(`task:${target.id}`);
        try {
          const state = await workRequest<{
            running: boolean;
            project_id: string | null;
          }>(`/api/v1/users/work/navigation/tasks/${target.id}`);
          current = {
            ...target,
            status: state.running ? "processing" : "finished",
            projectId: state.project_id || undefined,
          };
        } catch (e) {
          toast.error(workError(e));
          return;
        } finally {
          setBusy("");
        }
      }
      setDialog({ target: current, action: operation });
      setName(target.name);
      setError("");
      return;
    }
    const key = `${target.kind}:${target.id}`;
    setBusy(key);
    try {
      // Re-read on demand if the initial preference load failed.
      let current = pins;
      if (!ready) {
        const result = await workRequest<{ items: PinRow[] }>(
          "/api/v1/users/work/navigation/pins",
        );
        current = Object.fromEntries(
          result.items.map((p) => [`${p.kind}:${p.target_id}`, p.pinned_at]),
        );
      }
      const pinned = !current[key];
      await workRequest("/api/v1/users/work/navigation/pins", {
        method: "PUT",
        body: JSON.stringify({
          kind: target.kind,
          target_id: target.id,
          pinned,
        }),
      });
      const next = { ...current };
      if (pinned) next[key] = new Date().toISOString();
      else delete next[key];
      setPins(next);
      setReady(true);
      notifyNavigation({ ...target, action: "pin" });
    } catch (e) {
      toast.error(workError(e));
    } finally {
      setBusy("");
    }
  };
  const save = async () => {
    if (!dialog || busy) return;
    const { target, action: operation } = dialog;
    setBusy(`${target.kind}:${target.id}`);
    setError("");
    try {
      const endpoint = operation === "stop"
        ? "/api/v1/users/tasks/stop"
        : `/api/v1/users/${target.kind === "task" ? "tasks" : "projects"}/${target.id}`;
      await workRequest(
        endpoint,
        operation === "stop"
          ? { method: "PUT", body: JSON.stringify({ id: target.id }) }
          : operation === "rename"
            ? {
                method: "PUT",
                body: JSON.stringify(
                  target.kind === "task"
                    ? { title: name.trim() }
                    : { name: name.trim() },
                ),
              }
            : { method: "DELETE" },
      );
      notifyNavigation({
        ...target,
        action: operation,
        title: operation === "rename" ? name.trim() : undefined,
      });
      if (target.kind === "project") reloadProjects();
      if (operation === "delete") {
        setPins((prev) => {
          const next = { ...prev };
          delete next[`${target.kind}:${target.id}`];
          return next;
        });
        if (
          target.kind === "task" &&
          location.pathname === `/console/task/${target.id}`
        )
          navigate(
            target.projectId
              ? `/console/project/${target.projectId}`
              : "/console/work",
            { replace: true },
          );
        if (
          target.kind === "project" &&
          location.pathname === `/console/project/${target.id}`
        )
          navigate("/console/work", { replace: true });
      }
      setDialog(null);
      toast.success(
        t(
          operation === "stop"
            ? "navigationActions.stopped"
            : operation === "rename"
              ? "navigationActions.renamed"
              : "navigationActions.deleted",
        ),
      );
    } catch (e) {
      setError(workError(e));
    } finally {
      setBusy("");
    }
  };
  const running =
    dialog?.target.kind === "task" &&
    ["pending", "processing"].includes(dialog.target.status || "");
  return (
    <NavigationContext.Provider
      value={{
        pins, ready, busy,
        stoppingTaskId: dialog?.action === "stop" ? dialog.target.id : null,
        action: (item, op) => void action(item, op),
      }}
    >
      {children}
      <Dialog
        open={!!dialog}
        onOpenChange={(open) => {
          if (!open && !busy) setDialog(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {t(
                dialog?.action === "stop"
                  ? "navigationActions.stopTitle"
                  : dialog?.action === "rename"
                    ? "navigationActions.rename"
                    : dialog?.target.kind === "project"
                      ? "navigationActions.deleteProject"
                      : "navigationActions.deleteTask",
              )}
            </DialogTitle>
            <DialogDescription>
              {dialog?.action === "rename"
                ? dialog.target.name
                : t(
                    dialog?.action === "stop"
                      ? "navigationActions.stopHint"
                      : dialog?.target.kind === "project"
                        ? "navigationActions.projectDeleteHint"
                        : running
                          ? "navigationActions.runningDeleteHint"
                          : "navigationActions.taskDeleteHint",
                    { name: dialog?.target.name },
                  )}
            </DialogDescription>
          </DialogHeader>
          {dialog?.action === "rename" && (
            <Input
              autoFocus
              maxLength={200}
              aria-label={t("navigationActions.name")}
              value={name}
              disabled={!!busy}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.nativeEvent.isComposing) {
                  e.preventDefault();
                  if (name.trim()) void save();
                }
              }}
            />
          )}
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
          <DialogFooter>
            <Button
              variant="outline"
              disabled={!!busy}
              onClick={() => setDialog(null)}
              autoFocus={dialog?.action === "delete" || dialog?.action === "stop"}
            >
              {t("navigationActions.cancel")}
            </Button>
            <Button
              variant={dialog?.action === "delete" || dialog?.action === "stop" ? "destructive" : "default"}
              disabled={!!busy || (dialog?.action === "rename" && !name.trim())}
              onClick={() => void save()}
            >
              {busy && <Loader2 className="size-4 animate-spin" />}
              {t(
                busy
                  ? "navigationActions.working"
                  : dialog?.action === "stop"
                    ? "navigationActions.stopTask"
                    : dialog?.action === "rename"
                      ? "navigationActions.save"
                      : running
                        ? "navigationActions.stopDelete"
                        : "navigationActions.confirmDelete",
              )}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </NavigationContext.Provider>
  );
}

export function NavigationItemActions({
  target,
  children,
  className,
  trailingAction,
}: {
  target: Target;
  children?: ReactNode;
  className?: string;
  trailingAction?: ReactNode;
}) {
  const { t } = useTranslation();
  const { pins, busy, action } = useNavigationActions();
  const pinned = !!pins[`${target.kind}:${target.id}`];
  const items = [
    ...(target.kind === "project" && target.canWrite
      ? [{ id: "new", icon: Plus, label: t("navigationActions.newTask") }]
      : []),
    {
      id: "pin",
      icon: pinned ? PinOff : Pin,
      label: t(pinned ? "navigationActions.unpin" : "navigationActions.pin"),
    },
    ...(target.canWrite !== false
      ? [{ id: "rename", icon: Pencil, label: t("navigationActions.rename") }]
      : []),
    ...(target.kind === "task" && target.canWrite !== false &&
      ["pending", "processing"].includes(target.status || "")
      ? [{ id: "stop", icon: Square, label: t("navigationActions.stopTask") }]
      : []),
    ...(target.kind === "project" && target.canWrite
      ? [
          {
            id: "settings",
            icon: Settings2,
            label: t("navigationActions.settings"),
          },
        ]
      : []),
    ...(target.canDelete !== false
      ? [
          {
            id: "delete",
            icon: Trash2,
            label: t(
              target.kind === "project"
                ? "navigationActions.deleteProject"
                : "navigationActions.deleteTask",
            ),
          },
        ]
      : []),
  ];
  const menu = (context: boolean) =>
    items.map((item) => {
      const Item = context ? ContextMenuItem : DropdownMenuItem,
        Separator = context ? ContextMenuSeparator : DropdownMenuSeparator;
      return (
        <div key={item.id}>
          {item.id === "delete" && <Separator />}
          <Item
            disabled={!!busy}
            variant={item.id === "delete" || item.id === "stop" ? "destructive" : "default"}
            onSelect={() => action(target, item.id)}
          >
            <item.icon className="size-4" />
            {item.label}
          </Item>
        </div>
      );
    });
  const trigger = (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className={className || "size-7 shrink-0"}
          aria-label={t("navigationActions.more", { name: target.name })}
          disabled={!!busy}
          onClick={(e) => e.stopPropagation()}
        >
          {target.kind === "task" ? (
            <MoreVertical className="size-4" />
          ) : (
            <MoreHorizontal className="size-4" />
          )}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-44">
        {menu(false)}
      </DropdownMenuContent>
    </DropdownMenu>
  );
  if (!children) return trigger;
  return (
    <ContextMenu>
      <ContextMenuTrigger asChild>
        <div className="group/nav-row relative min-w-0">
          {children}
          <div className="absolute right-1 top-0.5 flex items-center gap-1">
            {trigger}
            {trailingAction}
          </div>
        </div>
      </ContextMenuTrigger>
      <ContextMenuContent className="min-w-44">{menu(true)}</ContextMenuContent>
    </ContextMenu>
  );
}

export function projectTarget(project: WorkProject, uid?: string): Target {
  const owner = project.user?.id === uid,
    write =
      owner ||
      !!project.collaborators?.some(
        (m) => m.id === uid && m.permission === "read_write",
      );
  return {
    kind: "project",
    id: project.id!,
    name: project.name || "",
    canWrite: write,
    canDelete: owner,
  };
}
export function taskTarget(task: WorkTask, projectId?: string): Target {
  return {
    kind: "task",
    id: task.id!,
    name: getTaskDisplayName(task),
    status: task.status,
    projectId: projectId || task.project_id || undefined,
  };
}
