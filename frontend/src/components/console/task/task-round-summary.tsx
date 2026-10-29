import { IconChevronRight } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"
import { getTaskRoundDuration, type TaskRound } from "./task-round-display"

export function TaskRoundSummary({ round, expanded, hasProcess, onToggle }: {
  round: TaskRound
  expanded: boolean
  hasProcess: boolean
  onToggle: () => void
}) {
  const { t } = useTranslation()
  const duration = getTaskRoundDuration(round)
  const seconds = duration === null ? 0 : Math.floor(duration / 1000)
  const durationText = seconds >= 3600
    ? t("taskDetail.roundDisplay.durationHours", { hours: Math.floor(seconds / 3600), minutes: Math.floor(seconds % 3600 / 60), seconds: seconds % 60 })
    : seconds >= 60
      ? t("taskDetail.roundDisplay.durationMinutes", { minutes: Math.floor(seconds / 60), seconds: seconds % 60 })
      : t("taskDetail.roundDisplay.durationSeconds", { seconds })
  const stateLabel = round.status === "failed" ? t("taskDetail.roundDisplay.failed")
    : round.status === "canceled" ? t("taskDetail.roundDisplay.canceled")
      : round.status === "unknown" ? t("taskDetail.roundDisplay.ended") : ""
  const label = duration === null
    ? stateLabel || t("taskDetail.roundDisplay.finished")
    : t("taskDetail.roundDisplay.elapsed", { duration: durationText })

  return <div className="flex flex-wrap items-center gap-x-3 px-1 py-2" data-round-summary={round.id}>
    {stateLabel && duration !== null && <span className={cn("text-xs", round.status === "failed" ? "text-destructive" : "text-muted-foreground")}>{stateLabel}</span>}
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          className="inline-flex min-h-8 items-center gap-1.5 rounded-sm text-xs text-muted-foreground hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring disabled:cursor-default disabled:hover:text-muted-foreground [@media(pointer:coarse)]:min-h-11"
          aria-label={hasProcess ? `${label} · ${t(expanded ? "taskDetail.roundDisplay.collapse" : "taskDetail.roundDisplay.expand")}` : label}
          aria-expanded={hasProcess ? expanded : undefined}
          disabled={!hasProcess}
          onClick={onToggle}
        >
          <span className="tabular-nums">{label}</span>
          {hasProcess && <IconChevronRight aria-hidden="true" className={cn("size-3.5 transition-transform motion-reduce:transition-none", expanded && "rotate-90")} />}
        </button>
      </TooltipTrigger>
      <TooltipContent>{t("taskDetail.roundDisplay.durationHint")}</TooltipContent>
    </Tooltip>
  </div>
}
