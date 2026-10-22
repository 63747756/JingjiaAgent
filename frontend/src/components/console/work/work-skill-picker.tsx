import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  ALL_SKILLS_TAG,
  TaskSkillPickerBody,
  type SkillForPicker,
} from "@/components/console/task/task-skill-selector";
import type { WorkSkill } from "@/lib/work-api";

export type WorkPickerSkill = Partial<WorkSkill> &
  SkillForPicker & { id: string; name: string; origins?: string[] };
export function WorkSkillPicker({
  skills,
  selected,
  onChange,
  disabled = false,
  className = "h-72",
}: {
  skills: WorkPickerSkill[];
  selected: string[];
  onChange: (id: string, checked: boolean) => void;
  disabled?: boolean;
  className?: string;
}) {
  const { t } = useTranslation();
  const [tag, setTag] = useState(ALL_SKILLS_TAG);
  const items = useMemo(
    () =>
      skills.map((s) => {
        const origins = (s.origins || []).map((o) =>
          t(`skillsExperience.origin.${o}`),
        );
      if(!origins.length && s.scope?.type==="team")origins.push(t("skillsExperience.origin.team"));
      if(!origins.length && s.scope?.type==="global")origins.push(t("skillsExperience.origin.global"));
        if (s.source_type === "builtin")
          origins.push(t("skillsExperience.origin.builtin"));
        if (s.source_type === "market")
          origins.push(t("skillsExperience.origin.market"));
        return { ...s, origin_label: [...new Set(origins)].join(" · ") };
      }),
    [skills, t],
  );
  const tags = useMemo(
    () => [
      ALL_SKILLS_TAG,
      ...[...new Set(skills.flatMap((s) => s.tags || []))].sort(),
    ],
    [skills],
  );
  return (
    <div
      className={`flex min-h-0 min-w-0 flex-col ${className}`}
      onKeyDown={(e) => e.stopPropagation()}
    >
      <TaskSkillPickerBody
        active
        showForced
        skills={items}
        selectedSkills={selected}
        skillTags={tags}
        activeSkillTag={tags.includes(tag) ? tag : ALL_SKILLS_TAG}
        onActiveSkillTagChange={setTag}
        onSkillChange={(id, checked) => {
          if (!disabled) onChange(id, checked);
        }}
      />
    </div>
  );
}
