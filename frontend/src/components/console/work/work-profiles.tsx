import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { BriefcaseBusiness, Plus, Pencil, Star } from "lucide-react";
import { toast } from "sonner";
import type { DomainModel, DomainHost, DomainImage } from "@/api/Api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { WorkEmpty, WorkError, WorkLoading } from "./work-common";
import {
  ProfileEditor,
  type ExecutionResources,
} from "./assistant-profile-editor";
import { useWorkManager } from "./use-work-manager";
import {
  workRequest,
  workItems,
  workError,
  workRequestID,
  type WorkProfile,
  type WorkSkill,
} from "@/lib/work-api";

const emptyResources: ExecutionResources = {
  models: [],
  hosts: [],
  images: [],
};
const newProfile = (
  teamID: string,
  t: (key: string) => string,
): WorkProfile => ({
  team_id: teamID,
  name: t("assistantProfiles.defaultName"),
  description: t("assistantProfiles.defaultDescription"),
  instructions: "",
  task_presets: [
    {
      id: workRequestID(),
      name: t("assistantProfiles.defaultTaskName"),
      description: t("assistantProfiles.defaultTaskDescription"),
      icon: "sparkles",
      enabled: true,
    },
  ],
  is_default: false,
  enabled: false,
  model_id: "",
  host_id: "",
  image_id: "",
  cli_name: "opencode",
  required_skill_ids: [],
  mentionable_skill_ids: [],
  core: 2,
  memory: 8 * 1073741824,
});

export function WorkProfiles() {
  const { t } = useTranslation();
  const { teams, error: teamError, load: reloadTeam } = useWorkManager();
  const teamID = teams[0]?.team_id || "";
  const [items, setItems] = useState<WorkProfile[]>([]);
  const [skills, setSkills] = useState<WorkSkill[]>([]);
  const [resources, setResources] =
    useState<ExecutionResources>(emptyResources);
  const [editing, setEditing] = useState<WorkProfile | null>(null);
  const [loading, setLoading] = useState(true);
  const [savingProfileID, setSavingProfileID] = useState("");
  const mutating = Boolean(savingProfileID);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    const results = await Promise.allSettled([
      workRequest<{ items: WorkProfile[] }>("/api/v1/users/work/profiles"),
      workRequest<{ models: DomainModel[] }>("/api/v1/teams/models"),
      workRequest<{ hosts: DomainHost[] }>("/api/v1/teams/hosts"),
      workRequest<{ images: DomainImage[] }>("/api/v1/teams/images"),
    ]);
    if (results[0].status === "fulfilled")
      setItems(
        workItems(results[0].value).map((profile) => ({
          ...profile,
          required_skill_ids: profile.required_skill_ids || [],
          mentionable_skill_ids: profile.mentionable_skill_ids || [],
          instructions: profile.instructions || "",
          task_presets: profile.task_presets || [],
        })),
      );
    else setItems([]);
    setResources({
      models:
        results[1].status === "fulfilled"
          ? (results[1].value.models || []).filter(
              (model) =>
                model.owner?.type !== "public" && model.is_hidden !== true,
            )
          : [],
      hosts:
        results[2].status === "fulfilled"
          ? (results[2].value.hosts || []).filter(
              (host) =>
                host.owner?.type !== "public" &&
                !host.id?.startsWith("public_host"),
            )
          : [],
      images:
        results[3].status === "fulfilled"
          ? (results[3].value.images || []).filter(
              (image) => image.owner?.type !== "public",
            )
          : [],
    });
    const failure = results.find((result) => result.status === "rejected");
    if (failure?.status === "rejected") setError(workError(failure.reason));
    setLoading(false);
  }, []);
  const loadSkills = useCallback(async () => {
    if (!teamID) {
      setSkills([]);
      return;
    }
    try {
      const result = await workRequest<{ items: WorkSkill[] }>(
        `/api/v1/teams/work/skills?team_id=${encodeURIComponent(teamID)}`,
      );
      setSkills(
        workItems(result).filter(
          (skill) =>
            (skill.scope.type === "team" || skill.scope.type === "global") &&
            skill.enabled &&
            skill.publication_state === "published",
        ),
      );
    } catch (e) {
      setSkills([]);
      setError(workError(e));
    }
  }, [teamID]);
  useEffect(() => {
    void load();
  }, [load]);
  useEffect(() => {
    void loadSkills();
  }, [loadSkills]);
  const teamProfiles = items.filter((item) => item.team_id === teamID);
  const currentDefault = teamProfiles.find(
    (item) => item.enabled && item.is_default,
  );
  const setDefault = async (id: string) => {
    if (!teamID || mutating) return;
    const selected = currentDefault?.id === id ? null : id;
    setSavingProfileID(id);
    setError("");
    try {
      await workRequest("/api/v1/users/work/profiles/default", {
        method: "PUT",
        body: JSON.stringify({
          team_id: teamID,
          profile_id: selected,
        }),
      });
      toast.success(
        t(
          !selected
            ? "assistantProfiles.defaultCleared"
            : "assistantProfiles.defaultSaved",
        ),
      );
      setItems((previous) =>
        previous.map((profile) =>
          profile.team_id === teamID
            ? { ...profile, is_default: profile.id === selected }
            : profile,
        ),
      );
    } catch (e) {
      setError(workError(e));
    } finally {
      setSavingProfileID("");
    }
  };
  const setEnabled = async (profile: WorkProfile, enabled: boolean) => {
    if (!profile.id || mutating) return;
    setSavingProfileID(profile.id);
    setError("");
    try {
      const saved = await workRequest<{
        id: string;
        enabled: boolean;
        is_default: boolean;
      }>(
        `/api/v1/users/work/profiles/${encodeURIComponent(profile.id)}/status`,
        { method: "PUT", body: JSON.stringify({ team_id: teamID, enabled }) },
      );
      setItems((previous) =>
        previous.map((item) =>
          item.id === saved.id
            ? { ...item, enabled: saved.enabled, is_default: saved.is_default }
            : item,
        ),
      );
      toast.success(
        t(
          enabled
            ? "assistantProfiles.assistantEnabled"
            : "assistantProfiles.assistantDisabled",
        ),
      );
      if (profile.is_default && !enabled)
        toast.message(t("assistantProfiles.defaultDisabled"));
    } catch (e) {
      setError(workError(e));
    } finally {
      setSavingProfileID("");
    }
  };
  return (
    <section className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">
            {t("assistantProfiles.title")}
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {t("assistantProfiles.pageHint")}
          </p>
        </div>
        <div className="flex gap-2">
          <Button
            size="sm"
            disabled={!teamID || loading || mutating}
            onClick={() => setEditing(newProfile(teamID, t))}
          >
            <Plus className="size-4" />
            {t("assistantProfiles.newAssistant")}
          </Button>
        </div>
      </div>
      {teamError && (
        <WorkError message={teamError} retry={() => void reloadTeam()} />
      )}
      {error && <WorkError message={error} retry={() => void load()} />}
      {loading ? (
        <WorkLoading />
      ) : !teamID ? (
        <WorkEmpty
          title={t("assistantProfiles.adminOnly")}
          description={t("assistantProfiles.adminOnly")}
        />
      ) : !items.some((item) => item.team_id === teamID) ? (
        <WorkEmpty
          title={t("assistantProfiles.emptyAssistants")}
          description={t("assistantProfiles.emptyAssistantsHint")}
        />
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {items
            .filter((item) => item.team_id === teamID)
            .map((profile) => (
              <article
                key={profile.id}
                className="flex min-w-0 flex-col gap-3 rounded-xl border bg-card p-4"
              >
                <div className="flex items-start gap-2">
                  <BriefcaseBusiness className="mt-1 size-5 shrink-0 text-primary" />
                  <h2 className="min-w-0 flex-1 break-words pt-1 text-sm font-medium">
                    {profile.name}
                  </h2>
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    className={`h-7 shrink-0 gap-1.5 rounded-full px-2 text-xs ${profile.is_default && profile.enabled ? "text-primary" : "text-muted-foreground"}`}
                    disabled={mutating || !profile.enabled || !profile.id}
                    title={
                      !profile.enabled
                        ? t("assistantProfiles.enableBeforeDefault")
                        : undefined
                    }
                    onClick={() => void setDefault(profile.id!)}
                  >
                    <Star
                      className={`size-3.5 ${profile.is_default && profile.enabled ? "fill-current" : ""}`}
                    />
                    {t(
                      profile.is_default && profile.enabled
                        ? "assistantProfiles.cancelDefault"
                        : "assistantProfiles.setDefault",
                    )}
                  </Button>
                </div>
                <p className="line-clamp-2 text-xs leading-5 text-muted-foreground">
                  {profile.description}
                </p>
                <div className="flex flex-wrap gap-2">
                  {profile.is_default && profile.enabled && (
                    <Badge variant="outline">
                      {t("assistantProfiles.defaultBadge")}
                    </Badge>
                  )}

                </div>
                <p className="break-words text-xs leading-5 text-muted-foreground">
                  {profile.task_presets
                    .filter((task) => task.enabled)
                    .map((task) => task.name)
                    .join(" · ")}
                </p>
                <div className="mt-auto flex items-center justify-between gap-3 border-t pt-3">
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={mutating}
                    onClick={() => setEditing(profile)}
                  >
                    <Pencil className="size-3.5" />
                    {t("assistantProfiles.editConfiguration")}
                  </Button>
                  <div className="flex shrink-0 items-center gap-2">
                    <span className="text-xs text-muted-foreground">
                      {t(
                        profile.enabled
                          ? "assistantProfiles.enabled"
                          : "assistantProfiles.disabled",
                      )}
                    </span>
                    <Switch
                      aria-label={t("assistantProfiles.cardEnabled", {
                        name: profile.name,
                      })}
                      checked={profile.enabled}
                      disabled={mutating || !profile.id}
                      onCheckedChange={(enabled) =>
                        void setEnabled(profile, enabled)
                      }
                    />
                  </div>
                </div>
              </article>
            ))}
        </div>
      )}
      {editing && (
        <ProfileEditor
          initial={editing}
          resources={resources}
          skills={skills}
          onClose={() => setEditing(null)}
          onSaved={(saved) => {
            setEditing(null);
            toast.success(t("assistantProfiles.saved"));
            if (editing.is_default && !saved.enabled)
              toast.message(t("assistantProfiles.defaultDisabled"));
            void load();
          }}
        />
      )}
    </section>
  );
}
