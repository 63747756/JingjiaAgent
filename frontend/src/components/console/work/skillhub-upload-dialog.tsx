import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ExternalLink, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import {
  workRequest,
  workPost,
  workError,
  WorkApiError,
  type WorkSkill,
} from "@/lib/work-api";
import { WorkError, WorkLoading } from "./work-common";
export type HubStatus = {
  configured: boolean;
  bound: boolean;
  url: string;
  namespace: string;
  account?: string;
};
type Precheck = {
  valid: boolean;
  errors: string[];
  warnings: string[];
  resolvedSlug: string;
  resolvedVersion: string;
  namespace: string;
  skill_name: string;
};
type UploadResult = { status: string; url: string; version: string };
const base = "/api/v1/users/work";
export function SkillHubUploadDialog({
  skill,
  onClose,
}: {
  skill: WorkSkill;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [status, setStatus] = useState<HubStatus | null>(null),
    [username, setUsername] = useState(""),
    [password, setPassword] = useState(""),
    [version, setVersion] = useState("1.0.0"),
    [preview, setPreview] = useState<Precheck | null>(null),
    [result, setResult] = useState<UploadResult | null>(null),
    [warnings, setWarnings] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  useEffect(() => {
    const c = new AbortController();
    workRequest<HubStatus>(base + "/skillhub/status", { signal: c.signal })
      .then(setStatus)
      .catch((e) => {
        if (!c.signal.aborted) setError(workError(e));
      });
    return () => c.abort();
  }, []);
  const run = async (action: "bind" | "unbind" | "preview" | "upload") => {
    setBusy(true);
    setError("");
    try {
      if (action === "bind") {
        const value = password;
        setPassword("");
        setStatus(
          await workPost<HubStatus>(base + "/skillhub/binding", {
            username,
            password: value,
          }),
        );
      }
      if (action === "unbind") {
        await workRequest(base + "/skillhub/binding", { method: "DELETE" });
        setStatus((s) => (s ? { ...s, bound: false } : s));
        setPreview(null);
      }
      if (action === "preview")
        setPreview(
          await workPost<Precheck>(
            base + `/skills/${skill.id}/skillhub/validate`,
            { version },
          ),
        );
      if (action === "upload")
        setResult(
          await workPost<UploadResult>(
            base + `/skills/${skill.id}/skillhub/upload`,
            { version, confirm_warnings: warnings },
          ),
        );
    } catch (e) {
      if (e instanceof WorkApiError && e.requiresBinding) {
        setStatus((s) => (s ? { ...s, bound: false } : s));
        setPreview(null);
      }
      setError(workError(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onOpenChange={(v) => {
        if (!v && !busy) onClose();
      }}
    >
      <DialogContent className="flex max-h-[90dvh] flex-col sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("skillsExperience.uploadToHub")}</DialogTitle>
          <DialogDescription>
            {t("skillsExperience.uploadHint")}
          </DialogDescription>
        </DialogHeader>
        <div className="min-h-0 space-y-4 overflow-y-auto">
          {!status && !error && <WorkLoading />}
          {status && !status.configured && (
            <p className="text-sm">{t("skillsExperience.notConfigured")}</p>
          )}
          {status?.configured && !status.bound && (
            <div className="space-y-3">
              <p className="text-sm">{t("skillsExperience.bindingHint")}</p>
              <label className="block text-sm">
                {t("skillsExperience.account")}
                <Input
                  autoComplete="username"
                  value={username}
                  disabled={busy}
                  onChange={(e) => setUsername(e.target.value)}
                />
              </label>
              <label className="block text-sm">
                {t("skillsExperience.password")}
                <Input
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  disabled={busy}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </label>
              <Button
                disabled={busy || !username || !password}
                onClick={() => void run("bind")}
              >
                {t("skillsExperience.bind")}
              </Button>
            </div>
          )}
          {status?.bound && !result && (
            <>
              <div className="flex items-center justify-between">
                <span className="text-sm">
                  {t("skillsExperience.boundAs", { name: status.account })}
                </span>
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={busy}
                  onClick={() => void run("unbind")}
                >
                  {t("skillsExperience.unbind")}
                </Button>
              </div>
              <p className="text-sm">
                {skill.name} → {status.namespace}
              </p>
              <label className="block text-sm">
                {t("skillsExperience.version")}
                <Input
                  value={version}
                  disabled={busy}
                  onChange={(e) => {
                    setVersion(e.target.value);
                    setPreview(null);
                    setWarnings(false);
                  }}
                />
              </label>
              <Button
                variant="outline"
                disabled={busy || !version}
                onClick={() => void run("preview")}
              >
                {t("skillsExperience.validateUpload")}
              </Button>
              {preview && (
                <div className="space-y-2 rounded-lg border p-3">
                  <p className="text-sm">
                    @{preview.namespace}/{preview.resolvedSlug} ·{" "}
                    {preview.resolvedVersion}
                  </p>
                  {preview.errors?.map((e) => (
                    <p key={e} className="text-xs text-destructive">
                      {e}
                    </p>
                  ))}
                  {preview.warnings?.map((w) => (
                    <p key={w} className="text-xs text-muted-foreground">
                      {w}
                    </p>
                  ))}
                  {!!preview.warnings?.length && !preview.errors?.length && (
                    <label className="flex items-center gap-2 text-sm">
                      <input
                        type="checkbox"
                        checked={warnings}
                        onChange={(e) => setWarnings(e.target.checked)}
                      />
                      {t("skillsExperience.confirmWarnings")}
                    </label>
                  )}
                  {preview.valid && (
                    <Badge variant="secondary">
                      {t("skillsExperience.valid")}
                    </Badge>
                  )}
                </div>
              )}
            </>
          )}
          {result && (
            <div className="space-y-3">
              <p className="text-sm">
                {t("skillsExperience.uploaded")} ·{" "}
                {t(`skillsExperience.status.${result.status}`, {
                  defaultValue: result.status,
                })}
              </p>
              <Button asChild variant="outline">
                <a href={result.url} target="_blank" rel="noopener noreferrer">
                  <ExternalLink className="size-4" />
                  {t("skillsExperience.openHub")}
                </a>
              </Button>
            </div>
          )}
          {error && <WorkError message={error} />}
        </div>
        <div className="flex justify-end gap-2 border-t pt-4">
          <Button variant="outline" disabled={busy} onClick={onClose}>
            {t("skillsExperience.close")}
          </Button>
          {status?.bound && !result && (
            <Button
              disabled={
                busy ||
                !preview ||
                !!preview.errors?.length ||
                (!preview.valid && !warnings)
              }
              onClick={() => void run("upload")}
            >
              {busy && <Loader2 className="size-4 animate-spin" />}
              {t("skillsExperience.confirmUpload")}
            </Button>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
