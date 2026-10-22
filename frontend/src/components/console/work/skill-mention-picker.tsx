import { useState } from "react";
import { Link } from "react-router-dom";
import { BookOpen, Loader2, Plus, Settings2, X, Paperclip, FolderOpen, ArrowLeft, ChevronRight } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuSub, DropdownMenuSubContent, DropdownMenuSubTrigger, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { useIsMobile } from "@/hooks/use-mobile";
import type { WorkSkill } from "@/lib/work-api";
import { WorkFilePickerBody, type WorkFilePickerProps } from "./work-file-picker";

export type SkillMention = { id: string; name: string; version_id?: string; version?: string };

export function skillToMention(skill: WorkSkill): SkillMention {
  return { id: skill.id, name: skill.name, version_id: skill.active_version_id, version: skill.versions.find((v) => v.id === skill.active_version_id)?.version };
}

export function SkillMentionChip({ mention, onRemove, disabled }: { mention: SkillMention; onRemove?: () => void; disabled?: boolean }) {
  const { t } = useTranslation();
  return <span className="inline-flex max-w-full items-center gap-1.5 rounded-md bg-primary/5 px-2 py-1 text-sm text-primary" data-skill-mention={mention.id}>
    <BookOpen className="size-3.5 shrink-0" />
    <span className="max-w-[min(22rem,65vw)] truncate" title={mention.name}>{mention.name}</span>
    {onRemove && <button type="button" disabled={disabled} aria-label={t("skillMentions.remove", { name: mention.name })} onClick={onRemove} className="rounded p-0.5 hover:bg-primary/10"><X className="size-3.5" /></button>}
  </span>;
}

export function SkillMentionPickerBody({ skills, loading, error, onSelect }: { skills: WorkSkill[]; loading?: boolean; error?: string; onSelect: (skill: WorkSkill) => void }) {
  const { t } = useTranslation();
  const [query, setQuery] = useState("");
  const visible = skills.filter((skill) => [skill.name, skill.description, ...(skill.tags || [])].join(" ").toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()));
  return <>
    <Input value={query} onChange={(e) => setQuery(e.target.value)} onKeyDown={(e) => e.stopPropagation()} aria-label={t("skillMentions.search")} placeholder={t("skillMentions.search")} className="mb-1 h-8 border-0 bg-muted/40 shadow-none" />
    <DropdownMenuSeparator />
    <div className="max-h-[min(20rem,40dvh)] overflow-y-auto overscroll-contain">
      {loading ? <p role="status" className="flex items-center gap-2 px-2 py-3 text-xs text-muted-foreground"><Loader2 className="size-3.5 animate-spin" />{t("assistantTasks.loading")}</p> : error ? <p role="alert" className="px-2 py-3 text-xs text-destructive">{error}</p> : visible.length ? ["assistant", "personal"].map((origin) => {
        const group = visible.filter((skill) => (skill.origins?.includes("assistant") ? "assistant" : "personal") === origin);
        return group.length > 0 && <div key={origin}>
          <div className="px-2 pb-1 pt-2 text-xs font-medium text-muted-foreground">{t(`skillMentions.group.${origin}`)}</div>
          {group.map((skill) => <DropdownMenuItem key={skill.id} onSelect={() => onSelect(skill)} className="items-start gap-2 rounded-lg py-2">
            <BookOpen className="mt-0.5 size-4 shrink-0 text-primary" /><span className="min-w-0 flex-1"><span className="flex items-center gap-2"><span className="min-w-0 flex-1 truncate">{skill.name}</span>{origin === "assistant" && <span className="shrink-0 text-[10px] text-muted-foreground">{t("skillMentions.automatic")}</span>}</span><span className="mt-0.5 block truncate text-xs text-muted-foreground">{skill.description}</span></span>
          </DropdownMenuItem>)}
        </div>;
      }) : <p className="px-2 py-3 text-xs text-muted-foreground">{t(query ? "skillMentions.noMatches" : "skillMentions.empty")}</p>}
    </div>
    <DropdownMenuSeparator />
    <DropdownMenuItem asChild><Link to="/console/skills"><Settings2 className="size-4" />{t("skillMentions.manage")}</Link></DropdownMenuItem>
  </>;
}

export function SkillMentionMenu({ disabled, onOpen, onUpload, projectFiles, compact, skills, loading, error, onSelect }: { disabled?: boolean; onOpen: () => void; onUpload?: () => void; projectFiles?: WorkFilePickerProps; compact?: boolean; skills: WorkSkill[]; loading?: boolean; error?: string; onSelect: (skill: WorkSkill) => void }) {
  const { t } = useTranslation();
  const mobile = useIsMobile();
  const [open, setOpen] = useState(false);
  const [panel, setPanel] = useState<"skills" | "files" | null>(null);
  const body = <SkillMentionPickerBody skills={skills} loading={loading} error={error} onSelect={(skill) => { onSelect(skill); setOpen(false); }} />;
  const filesBody = projectFiles && <WorkFilePickerBody {...projectFiles} onSelect={async (resource, versionID) => {
    const accepted = await projectFiles.onSelect(resource, versionID);
    if (projectFiles.single && accepted !== false) setOpen(false);
    return accepted !== false;
  }} />;
  return <DropdownMenu open={open} onOpenChange={(value) => { setOpen(value); setPanel(null); if (value) onOpen(); }}>
    <DropdownMenuTrigger asChild><Button type="button" variant="ghost" size="icon-sm" disabled={disabled} aria-label={t("assistantTasks.addContext")} className={compact ? "size-8 rounded-lg max-sm:size-11" : "rounded-lg max-sm:size-11"}><Plus className="size-4" /></Button></DropdownMenuTrigger>
    <DropdownMenuContent align="start" className={panel ? "w-80 max-w-[calc(100vw-2rem)] rounded-xl" : "w-52 rounded-xl"}>
      {mobile && panel ? <><DropdownMenuItem onSelect={(e) => { e.preventDefault(); setPanel(null); }}><ArrowLeft className="size-4" />{t("assistantTasks.backToMenu")}</DropdownMenuItem>{panel === "files" ? filesBody : body}</> : <>
        {onUpload && <DropdownMenuItem onSelect={onUpload}><Paperclip className="size-4" />{t("assistantTasks.uploadAttachment")}</DropdownMenuItem>}
        {projectFiles && (mobile ? <DropdownMenuItem onSelect={(e) => { e.preventDefault(); setPanel("files"); }}><FolderOpen className="size-4" />{t("assistantTasks.projectFiles")}<ChevronRight className="ml-auto size-4" /></DropdownMenuItem> : <DropdownMenuSub><DropdownMenuSubTrigger><FolderOpen className="size-4" />{t("assistantTasks.projectFiles")}</DropdownMenuSubTrigger><DropdownMenuSubContent className="w-80 max-w-[calc(100vw-2rem)] rounded-xl" collisionPadding={8}>{filesBody}</DropdownMenuSubContent></DropdownMenuSub>)}
        {(onUpload || projectFiles) && <DropdownMenuSeparator />}
        {mobile ? <DropdownMenuItem onSelect={(e) => { e.preventDefault(); setPanel("skills"); }}><BookOpen className="size-4" />{t("assistantTasks.skills")}<ChevronRight className="ml-auto size-4" /></DropdownMenuItem> : <DropdownMenuSub><DropdownMenuSubTrigger><BookOpen className="size-4" />{t("assistantTasks.skills")}</DropdownMenuSubTrigger><DropdownMenuSubContent className="w-80 max-w-[calc(100vw-2rem)] rounded-xl">{body}</DropdownMenuSubContent></DropdownMenuSub>}
      </>}
    </DropdownMenuContent>
  </DropdownMenu>;
}
