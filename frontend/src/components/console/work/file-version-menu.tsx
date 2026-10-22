import { ChevronDown, History, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { workTime } from "@/lib/work-api";
import type { FileVersionOption } from "@/lib/work-files";

export function FileVersionMenu({ filename, versions, selectedID, currentID, disabled, onChange, display = "history", versionCount, onOpenChange, loading, error, onRetry }: {
  filename: string; versions: FileVersionOption[]; selectedID: string; currentID: string;
  disabled?: boolean; onChange: (id: string) => void; display?: "history" | "version";
  versionCount?: number; onOpenChange?: (open: boolean) => void;
  loading?: boolean; error?: string; onRetry?: () => void;
}) {
  const { t } = useTranslation();
  const selected = versions.find((version) => version.id === selectedID) || versions.find((version) => version.id === currentID) || versions[0];
  const tagClass = "inline-flex h-7 shrink-0 items-center justify-center gap-1 rounded-md border border-transparent bg-muted/70 px-2 text-xs font-normal tabular-nums text-muted-foreground";
  const shortLabel = t("workFiles.versionShort", { version: selected?.number || 1 });
  if ((versionCount ?? versions.length) < 2) return display === "version" ? <span data-slot="file-version-label" className={tagClass} title={t("workFiles.version", { version: selected?.number || 1 })}>{shortLabel}</span> : null;
  const historical = selected?.id !== currentID;
  return <div className="flex shrink-0 items-center gap-1.5"><DropdownMenu onOpenChange={onOpenChange}>
    <DropdownMenuTrigger asChild>
      <Button type="button" variant="ghost" size="sm" disabled={disabled} aria-label={t("workFiles.versionMenu", { name: filename })} className={display === "version" ? tagClass : "h-8 gap-1.5 px-2 text-xs font-normal text-muted-foreground"}>
        {display === "version" ? <>{shortLabel}<ChevronDown className="size-3" /></> : <><History className="size-3.5" />{historical ? t("workFiles.version", { version: selected?.number }) : t("workFiles.history")}</>}
      </Button>
    </DropdownMenuTrigger>
    <DropdownMenuContent align="start" collisionPadding={8} className="max-h-72 w-64 max-w-[calc(100vw-2rem)] overflow-y-auto">
      {loading && <div role="status" className="flex items-center gap-2 px-2 py-3 text-xs text-muted-foreground"><Loader2 className="size-3.5 animate-spin" />{t("workArtifacts.loadingVersions")}</div>}
      {error && <><p role="alert" className="px-2 py-2 text-xs text-destructive">{error}</p><DropdownMenuItem onSelect={(event) => { event.preventDefault(); onRetry?.(); }}>{t("workArtifacts.retry")}</DropdownMenuItem></>}
      <DropdownMenuRadioGroup value={selected?.id} onValueChange={onChange}>
        {versions.map((version) => <DropdownMenuRadioItem key={version.id} value={version.id} className="items-start py-2">
          {display === "version" ? <span className="flex min-w-0 flex-1 items-center gap-2 text-xs"><span className="min-w-6 font-medium tabular-nums">{t("workFiles.versionShort", { version: version.number })}</span>
            {version.id === currentID && <span className="rounded bg-muted px-1 py-0.5 text-[10px] text-muted-foreground">{t("workFiles.latest")}</span>}
            {version.createdAt && <span className="ml-auto whitespace-nowrap text-muted-foreground">{workTime(version.createdAt)}</span>}
          </span> : <span className="min-w-0"><span className="block">{t("workFiles.version", { version: version.number })}{version.id === currentID ? ` · ${t("workFiles.latest")}` : ""}</span>
            {version.createdAt && <span className="mt-0.5 block text-xs text-muted-foreground">{workTime(version.createdAt)}</span>}
          </span>}
        </DropdownMenuRadioItem>)}
      </DropdownMenuRadioGroup>
    </DropdownMenuContent>
  </DropdownMenu>{display === "version" && historical && <span className="text-[11px] text-muted-foreground">{t("workFiles.historical")}</span>}</div>;
}
