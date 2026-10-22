import { SkillMentionMenu } from "./skill-mention-picker";
import type { WorkResource, WorkSkill } from "@/lib/work-api";

export function WorkContextMenu({ disabled, uploading, projectSelected, resources, resourcesLoading, contextError, skills, skillsLoading, skillError, versionIDs, onUpload, onSkillChange, onSkillsOpen, onVersionChange }: {
  disabled: boolean; uploading: boolean; projectSelected: boolean;
  resources: WorkResource[]; resourcesLoading: boolean; contextError: string;
  skills: WorkSkill[]; skillsLoading?: boolean; skillError?: string; versionIDs: string[];
  onUpload: () => void; onSkillChange: (skill: WorkSkill) => void; onSkillsOpen: () => void;
  onVersionChange: (resource: WorkResource, version: string) => void;
}) {
  return <SkillMentionMenu compact disabled={disabled || uploading} onOpen={onSkillsOpen} onUpload={onUpload}
    projectFiles={projectSelected ? { resources, loading: resourcesLoading, error: contextError, selectedIDs: versionIDs, onSelect: onVersionChange } : undefined}
    skills={skills} loading={skillsLoading} error={skillError} onSelect={onSkillChange} />;
}
