import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { WorkSelect } from "./work-select";
import { Users, Save, Loader2, Bot } from "lucide-react";
import { toast } from "sonner";
import { useCommonData } from "@/components/console/data-provider";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  workError,
  workRequest,
  type WorkProject,
  type WorkConfig,
} from "@/lib/work-api";
import { WorkError } from "./work-common";
import { canManageProjectMembers, projectSettingsPayload } from "@/lib/project-settings";

export function WorkProjectSettings({
  project,
  onSaved,
}: {
  project: WorkProject;
  onSaved?: () => void;
}) {
  const { t } = useTranslation();
  const { user, members, reloadMembers, reloadProjects } = useCommonData();
  const canManageMembers = canManageProjectMembers(project, user.id);
  const [name, setName] = useState(project.name || "");
  const [rules, setRules] = useState(project.rules || "");
  const [profileID, setProfileID] = useState(project.default_profile_id || "");
  const [profiles, setProfiles] = useState<NonNullable<WorkConfig["profiles"]>>(
    [],
  );
  const [collaborators, setCollaborators] = useState<
    Record<string, "read_only" | "read_write">
  >(
    Object.fromEntries(
      (project.collaborators || [])
        .filter((member) => member.id)
        .map((member) => [
          member.id!,
          member.permission === "read_write" ? "read_write" : "read_only",
        ]),
    ),
  );
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (canManageMembers) reloadMembers();
    workRequest<WorkConfig>("/api/v1/users/work/config")
      .then((c) =>
        setProfiles(
          (c.profiles || []).filter((p) => p.team_id === project.team_id),
        ),
      )
      .catch((e) => setError(workError(e)));
  }, [reloadMembers, canManageMembers, project.team_id]);
  const save = async () => {
    setSaving(true);
    setError("");
    try {
      await workRequest(
        `/api/v1/users/projects/${encodeURIComponent(project.id!)}`,
        {
          method: "PUT",
          body: JSON.stringify(projectSettingsPayload(project, user.id, {
            name, rules, profileID, collaborators,
          })),
        },
      );
      toast.success(t("projectSettings.saved"));
      reloadProjects();
      onSaved?.();
    } catch (e) {
      setError(workError(e));
    } finally {
      setSaving(false);
    }
  };
  const memberList = [...(project.collaborators || []), ...(canManageMembers ? members : [])].filter(
    (member, index, all) =>
      member.id && all.findIndex((row) => row.id === member.id) === index,
  );
  return (
    <section className="max-w-3xl space-y-5 rounded-xl border bg-card p-5">
      <div>
        <h2 className="text-lg font-semibold">项目设置</h2>
        <p className="mt-1 text-xs leading-5 text-muted-foreground">
          规范与默认助手用于之后的新任务。成员共享项目资料和发布成果，各自的任务与对话保持私有。
        </p>
      </div>
      <label className="block text-sm">
        项目名称
        <Input
          className="mt-2"
          value={name}
          onChange={(e) => setName(e.target.value)}
          disabled={saving}
        />
      </label>
      <label className="block text-sm">
        工作规范
        <Textarea
          className="mt-2 min-h-32"
          value={rules}
          onChange={(e) => setRules(e.target.value)}
          placeholder="业务背景、数据口径、内容风格、成果要求"
          disabled={saving}
        />
      </label>
      <fieldset>
        <legend className="text-sm font-medium">{t("skillsExperience.defaultAssistant")}</legend>
        <div className="mt-3"><WorkSelect
          label={t("skillsExperience.defaultAssistant")} value={profileID} disabled={saving}
          placeholder={t("skillsExperience.noDefaultAssistant")} searchPlaceholder={t("skillsExperience.searchAssistant")}
          emptyText={t("skillsExperience.noAssistants")} icon={Bot}
          options={[{id:"",label:t("skillsExperience.noDefaultAssistant")},...profiles.map((p)=>({id:p.id,label:p.name}))]}
          onChange={setProfileID}
        /></div>
      </fieldset>
      <fieldset disabled={saving || !canManageMembers}>
        <legend className="flex items-center gap-2 text-sm font-medium">
          <Users className="size-4" />
          项目成员
        </legend>
        <p className="mt-1 text-xs text-muted-foreground">
          只读成员可查看资料和成果；读写成员可上传资料、发布成果并发起自己的任务。
        </p>
        {!canManageMembers && <p className="mt-1 text-xs text-muted-foreground">{t("projectSettings.ownerManagesMembers")}</p>}
        <div className="mt-3 max-h-72 space-y-2 overflow-auto">
          {memberList.map((member) => (
            <div
              key={member.id}
              className="flex items-center justify-between gap-3 text-sm"
            >
              <span className="min-w-0 truncate">
                {member.name || member.email}
                {member.id === project.user?.id
                  ? " · 所有者"
                  : member.id === user.id
                    ? " · 我"
                    : ""}
              </span>
              <select
                aria-label={`${member.name || member.email} 的项目权限`}
                className="rounded-lg border bg-background px-2 py-1 text-xs"
                value={collaborators[member.id!] || "none"}
                disabled={saving || member.id === project.user?.id}
                onChange={(e) =>
                  setCollaborators((previous) => {
                    const result = { ...previous };
                    if (e.target.value === "none") delete result[member.id!];
                    else
                      result[member.id!] = e.target.value as
                        | "read_only"
                        | "read_write";
                    return result;
                  })
                }
              >
                <option value="none">未加入</option>
                <option value="read_only">只读</option>
                <option value="read_write">读写</option>
              </select>
            </div>
          ))}
        </div>
      </fieldset>
      {error && <WorkError message={error} />}
      <Button disabled={!name.trim() || saving} onClick={() => void save()}>
        {saving ? (
          <Loader2 className="size-4 animate-spin" />
        ) : (
          <Save className="size-4" />
        )}
        保存设置
      </Button>
    </section>
  );
}
