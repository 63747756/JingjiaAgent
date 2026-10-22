import { useState } from "react";
import { FileText, History, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { DropdownMenuCheckboxItem, DropdownMenuItem, DropdownMenuSeparator } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { resourceVersions } from "@/lib/work-files";
import { workSize, workTime, type WorkResource } from "@/lib/work-api";

export type WorkFilePickerProps = {
  resources: WorkResource[]; loading?: boolean; error?: string; selectedIDs?: string[];
  onSelect: (resource: WorkResource, versionID: string) => void | boolean | Promise<boolean>;
  single?: boolean; busy?: boolean;
};

export function WorkFilePickerBody({ resources, loading, error, selectedIDs = [], onSelect, single = false, busy = false }: WorkFilePickerProps) {
  const { t } = useTranslation();
  const [query, setQuery] = useState("");
  const [expanded, setExpanded] = useState<string | null>(null);
  const visible = resources.filter((resource) => resource.filename.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()));
  return <>
    <Input aria-label={t("assistantTasks.searchFiles")} placeholder={t("assistantTasks.searchFiles")} value={query}
      onChange={(event) => setQuery(event.target.value)} onKeyDown={(event) => event.stopPropagation()}
      className="mb-1 h-8 border-0 bg-muted/40 shadow-none" />
    <DropdownMenuSeparator />
    <div className="max-h-[min(20rem,40dvh)] overflow-y-auto overscroll-contain">
      {loading ? <p role="status" className="flex items-center gap-2 px-2 py-3 text-xs text-muted-foreground"><Loader2 className="size-3.5 animate-spin" />{t("assistantTasks.loading")}</p> : error ? <p role="alert" className="px-2 py-3 text-xs text-destructive">{error}</p> : !visible.length ? <p className="px-2 py-3 text-xs text-muted-foreground">{t(query ? "workFiles.noMatches" : "workFiles.noFiles")}</p> : visible.map((resource) => {
        const options = resourceVersions(resource);
        const selected = options.find((version) => selectedIDs.includes(version.id));
        const versionID = selected?.id || resource.current_version_id;
        const version = resource.versions?.find((item) => item.id === versionID);
        const label = <><FileText className="size-4 shrink-0 text-muted-foreground" /><span className="min-w-0 flex-1"><span className="block truncate" title={resource.filename}>{resource.filename}</span><span className="mt-0.5 block truncate text-xs text-muted-foreground">{workSize(version?.size)} · {workTime(version?.created_at)}{selected && selected.id !== resource.current_version_id ? ` · ${t("workFiles.version", { version: selected.number })}` : ""}</span></span></>;
        const choose = (id: string) => { void onSelect(resource, id); };
        return <div key={resource.id}>
          <div className="flex min-w-0 items-center gap-1">
            {single ? <DropdownMenuItem disabled={busy} onSelect={(event) => { event.preventDefault(); choose(versionID); }} className="min-w-0 flex-1 rounded-lg py-2">{label}</DropdownMenuItem> : <DropdownMenuCheckboxItem aria-label={t("workFiles.selectFile", { name: resource.filename })} checked={!!selected} disabled={busy} onSelect={(event) => { event.preventDefault(); choose(versionID); }} className="min-w-0 flex-1 rounded-lg py-2">{label}</DropdownMenuCheckboxItem>}
            {options.length > 1 && <DropdownMenuItem disabled={busy} aria-label={t("workFiles.versionMenu", { name: resource.filename })} aria-expanded={expanded === resource.id}
              onSelect={(event) => { event.preventDefault(); setExpanded((previous) => previous === resource.id ? null : resource.id); }} className="size-8 shrink-0 justify-center rounded-lg p-0"><History className="size-3.5 text-muted-foreground" /></DropdownMenuItem>}
          </div>
          {expanded === resource.id && <div className="mb-1 ml-3 border-l pl-2">
            {options.map((option) => <DropdownMenuCheckboxItem key={option.id} checked={selected?.id === option.id} disabled={busy}
              onSelect={(event) => { event.preventDefault(); choose(option.id); }} className="items-start rounded-lg py-2 text-xs">
              <span className="min-w-0"><span className="block">{t("workFiles.version", { version: option.number })}{option.id === resource.current_version_id ? ` · ${t("workFiles.latest")}` : ""}</span>
                {option.createdAt && <span className="mt-0.5 block text-muted-foreground">{workTime(option.createdAt)}</span>}</span>
            </DropdownMenuCheckboxItem>)}
          </div>}
        </div>;
      })}
    </div>
  </>;
}
