import { useCallback, useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Spinner } from "@/components/ui/spinner"
import type { TaskControlOperation } from "@/components/console/task/task-control-client"
import { apiRequest } from "@/utils/requestUtils"

import { filterSelectableSkillIds } from "./task-skill-selection"
import {
  ALL_SKILLS_TAG,
  type SkillForPicker,
  TaskSkillPickerBody,
} from "./task-skill-selector"

interface TaskSkillsUpdateDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  initialSkillIds: string[]
  pluginIds: string[]
  operation?: TaskControlOperation | null
  noticeKey: string | null
  disabled: boolean
  onSwitch: (
    skillIds: string[],
    pluginIds: string[],
  ) => void
}

export function TaskSkillsUpdateDialog({
  open,
  onOpenChange,
  initialSkillIds,
  pluginIds,
  operation,
  noticeKey,
  disabled,
  onSwitch,
}: TaskSkillsUpdateDialogProps) {
  const { t } = useTranslation()
  const [skillList, setSkillList] = useState<SkillForPicker[]>([])
  const [loading, setLoading] = useState(false)
  const [selectedSkills, setSelectedSkills] = useState<string[]>(initialSkillIds)
  const [activeSkillTag, setActiveSkillTag] = useState<string>(ALL_SKILLS_TAG)
  const submitting = !!operation

  useEffect(() => {
    if (!open) return
    let active = true
    setSelectedSkills(initialSkillIds)
    setLoading(true)
    apiRequest("v1SkillsList", {}, [], (resp) => {
      if (!active) return
      setLoading(false)
      if (resp.code === 0) {
        const skills = (resp.data || []) as SkillForPicker[]
        setSkillList(skills)
        setSelectedSkills((prev) => filterSelectableSkillIds(prev, skills))
      } else {
        toast.error(resp.message || t("taskWorkflow.toast.fetchSkillsFailed"))
      }
    })
    return () => { active = false }
  }, [open])

  const skillTags = useMemo(() => {
    const tagCountMap = new Map<string, number>()
    skillList.forEach((skill) => {
      ;(skill.tags || []).forEach((tag) => {
        tagCountMap.set(tag, (tagCountMap.get(tag) || 0) + 1)
      })
    })
    const sortedTags = Array.from(tagCountMap.keys()).sort(
      (a, b) => tagCountMap.get(b)! - tagCountMap.get(a)!,
    )
    return [ALL_SKILLS_TAG].concat(sortedTags)
  }, [skillList])

  useEffect(() => {
    if (!skillTags.includes(activeSkillTag)) {
      setActiveSkillTag(skillTags[0] || ALL_SKILLS_TAG)
    }
  }, [activeSkillTag, skillTags])

  const handleSkillChange = useCallback((skillId: string, checked: boolean) => {
    if (submitting) return
    setSelectedSkills((prev) => {
      const next = new Set(prev)
      if (checked) {
        next.add(skillId)
      } else {
        next.delete(skillId)
      }
      return Array.from(next)
    })
  }, [submitting])

  const handleSave = useCallback(() => {
    if (submitting || disabled || loading) return
    // Preserve the full resource declaration; the page owns operation completion.
    onSwitch(selectedSkills, pluginIds)
  }, [disabled, loading, onSwitch, pluginIds, selectedSkills, submitting])

  const renderBody = () => {
    if (loading) {
      return (
        <div className="flex h-40 items-center justify-center">
          <Spinner className="size-5" />
        </div>
      )
    }

    if (skillList.length === 0) {
      return (
        <div className="flex h-40 items-center justify-center text-sm text-muted-foreground">
          {t("taskDetail.chat.skillsDialog.empty")}
        </div>
      )
    }

    return (
      <fieldset disabled={submitting} className="flex h-80 min-h-0 min-w-0 max-w-full flex-col disabled:opacity-60">
        <TaskSkillPickerBody
          active={open && !submitting}
          selectedSkills={selectedSkills}
          skills={skillList}
          skillTags={skillTags}
          activeSkillTag={activeSkillTag}
          onActiveSkillTagChange={setActiveSkillTag}
          onSkillChange={handleSkillChange}
        />
      </fieldset>
    )
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t("taskDetail.chat.skillsDialog.title")}</DialogTitle>
          <DialogDescription>
            {t("taskDetail.chat.skillsDialog.description")}
          </DialogDescription>
        </DialogHeader>
        {renderBody()}
        {noticeKey && <p role="status" className="text-sm text-muted-foreground">{t(noticeKey)} {t("taskDetail.page.control.closeHint")}</p>}
        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
          >
            {t(submitting ? "taskDetail.common.close" : "taskDetail.common.cancel")}
          </Button>
          <Button type="button" onClick={() => void handleSave()} disabled={disabled || submitting || loading}>
            {submitting && <Spinner className="mr-2 size-4" />}
            {t("taskDetail.chat.skillsDialog.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
