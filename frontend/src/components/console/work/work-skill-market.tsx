import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Download, ExternalLink, Loader2, BookOpen } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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
  type WorkSkill,
} from "@/lib/work-api";
import { WorkEmpty, WorkError, WorkLoading } from "./work-common";
import type { HubStatus } from "./skillhub-upload-dialog";
type MarketSkill = {
  slug: string;
  displayName: string;
  summary: string;
  namespace: string;
  visibility: string;
  publishedVersion?: { version: string; status: string } | null;
};
type MarketPage = {
  items: MarketSkill[];
  page: number;
  size: number;
  total: number;
};
const base = "/api/v1/users/work";
export function WorkSkillMarket() {
  const { t } = useTranslation();
  const [status, setStatus] = useState<HubStatus | null>(null),
    [query, setQuery] = useState(""),
    [search, setSearch] = useState(""),
    [page, setPage] = useState(0),
    [data, setData] = useState<MarketPage | null>(null),
    [loading, setLoading] = useState(false),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(""),
    [installed, setInstalled] = useState<WorkSkill[]>([]),
    [detail, setDetail] = useState<MarketSkill | null>(null);
  useEffect(() => {
    const c = new AbortController();
    workRequest<HubStatus>(base + "/skillhub/status", { signal: c.signal })
      .then(setStatus)
      .catch((e) => {
        if (!c.signal.aborted) setError(workError(e));
      });
    workRequest<{ items: WorkSkill[] }>(base + "/skills", { signal: c.signal })
      .then((v) =>
        setInstalled(v.items.filter((s) => s.source_type === "market")),
      )
      .catch(() => {});
    return () => c.abort();
  }, []);
  useEffect(() => {
    if (!status?.configured) return;
    const c = new AbortController();
    setLoading(true);
    setError("");
    workRequest<MarketPage>(
      base +
        `/skillhub/market?q=${encodeURIComponent(search)}&page=${page}&size=20`,
      { signal: c.signal },
    )
      .then(setData)
      .catch((e) => {
        if (!c.signal.aborted) setError(workError(e));
      })
      .finally(() => {
        if (!c.signal.aborted) setLoading(false);
      });
    return () => c.abort();
  }, [status?.configured, search, page]);
  const url = (s: MarketSkill) =>
    `${status?.url}/space/${s.namespace}/${s.slug}`;
  const install = async (s: MarketSkill) => {
    if (!s.publishedVersion) return;
    setBusy(s.namespace + "/" + s.slug);
    try {
      const item = await workPost<WorkSkill>(base + "/skillhub/install", {
        namespace: s.namespace,
        slug: s.slug,
        version: s.publishedVersion.version,
      });
      setInstalled((prev) => [...prev.filter((v) => v.id !== item.id), item]);
      toast.success(t("skillsExperience.installed"));
    } catch (e) {
      toast.error(workError(e));
    } finally {
      setBusy("");
    }
  };
  const open = async (s: MarketSkill) => {
    setBusy(s.namespace + "/" + s.slug);
    try {
      const response = await workRequest<MarketSkill>(
        base +
          `/skillhub/market/${encodeURIComponent(s.namespace)}/${encodeURIComponent(s.slug)}`,
      );
      setDetail({ ...s, ...response });
    } catch (e) {
      toast.error(workError(e));
    } finally {
      setBusy("");
    }
  };
  return (
    <section className="w-full min-w-0 space-y-5">
      <form
        className="flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          setPage(0);
          setSearch(query);
        }}
      >
        <Input
          aria-label={t("skillsExperience.search")}
          placeholder={t("skillsExperience.search")}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <Button type="submit" disabled={loading || !status?.configured}>
          {t("skillsExperience.searchButton")}
        </Button>
      </form>
      {error && <WorkError message={error} />}{" "}
      {status && !status.configured && (
        <WorkEmpty
          title={t("skillsExperience.notConfigured")}
          description={t("skillsExperience.localAvailable")}
        />
      )}
      {loading ? (
        <WorkLoading />
      ) : data && !data.items.length ? (
        <WorkEmpty
          title={t("skillsExperience.marketEmpty")}
          description={t("skillsExperience.marketEmptyHint")}
        />
      ) : (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {data?.items.map((s) => {
            const local = installed.find((v) => v.source_url === url(s));
            const same = local?.source_version === s.publishedVersion?.version;
            return (
              <article
                key={s.namespace + "/" + s.slug}
                className="flex min-w-0 flex-col gap-3 rounded-xl border p-4"
              >
                <BookOpen className="size-5 text-primary" />
                <button
                  className="text-left text-sm font-medium"
                  onClick={() => void open(s)}
                >
                  {s.displayName || s.slug}
                </button>
                <p className="line-clamp-3 flex-1 text-xs text-muted-foreground">
                  {s.summary}
                </p>
                <p className="truncate text-xs text-muted-foreground">
                  @{s.namespace}/{s.slug} · {s.publishedVersion?.version}
                </p>
                <div className="flex justify-between gap-2">
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => void open(s)}
                    disabled={!!busy}
                  >
                    {t("skillsExperience.details")}
                  </Button>
                  <Button
                    size="sm"
                    disabled={
                      !!busy ||
                      same ||
                      !s.publishedVersion ||
                      s.visibility !== "PUBLIC"
                    }
                    onClick={() => void install(s)}
                  >
                    {busy === s.namespace + "/" + s.slug ? (
                      <Loader2 className="size-4 animate-spin" />
                    ) : (
                      <Download className="size-4" />
                    )}
                    {t(
                      same
                        ? "skillsExperience.alreadyInstalled"
                        : local
                          ? "skillsExperience.update"
                          : "skillsExperience.install",
                    )}
                  </Button>
                </div>
              </article>
            );
          })}
        </div>
      )}
      {data && (
        <div className="flex items-center justify-end gap-3">
          <Button
            variant="outline"
            size="sm"
            disabled={page === 0 || loading}
            onClick={() => setPage((p) => p - 1)}
          >
            {t("skillsExperience.previous")}
          </Button>
          <span className="text-xs">
            {page + 1} / {Math.max(1, Math.ceil(data.total / 20))}
          </span>
          <Button
            variant="outline"
            size="sm"
            disabled={(page + 1) * 20 >= data.total || loading}
            onClick={() => setPage((p) => p + 1)}
          >
            {t("skillsExperience.next")}
          </Button>
        </div>
      )}
      <Dialog
        open={!!detail}
        onOpenChange={(v) => {
          if (!v) setDetail(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{detail?.displayName || detail?.slug}</DialogTitle>
            <DialogDescription>{detail?.summary}</DialogDescription>
          </DialogHeader>
          {detail && (
            <>
              <p className="text-sm">
                @{detail.namespace}/{detail.slug} ·{" "}
                {detail.publishedVersion?.version}
              </p>
              <Button asChild variant="outline">
                <a href={url(detail)} target="_blank" rel="noopener noreferrer">
                  <ExternalLink className="size-4" />
                  {t("skillsExperience.openHub")}
                </a>
              </Button>
              <Button
                disabled={!!busy || !detail.publishedVersion}
                onClick={() => void install(detail)}
              >
                {t("skillsExperience.install")}
              </Button>
            </>
          )}
        </DialogContent>
      </Dialog>
    </section>
  );
}
