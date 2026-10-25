import type { ReactNode } from "react"
import { AlertCircle } from "lucide-react"
import { IconLoader } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

export interface TaskStatusBarProps {
  label: string
  description?: string
  executionTimeMs?: number
  failed?: boolean
  actions: ReactNode
}

export function TaskStatusBar({ label, description, executionTimeMs, failed = false, actions }: TaskStatusBarProps) {
  const { t } = useTranslation()
  return <div className="mb-2 flex min-w-0 flex-wrap items-center justify-between gap-3 rounded-md border bg-muted/40 px-3 py-2 text-xs">
    <div className="flex min-w-0 grow basis-64 items-start gap-2">
      {failed
        ? <AlertCircle className="size-4 shrink-0 text-destructive" />
        : <IconLoader className="size-4 shrink-0 animate-spin text-primary" />}
      <div className="flex min-w-0 flex-wrap items-baseline gap-x-3 gap-y-1 leading-4">
        <span role={failed ? "alert" : "status"} className="min-w-0 break-words font-medium">{label}</span>
        {executionTimeMs !== undefined && <span className="shrink-0 tabular-nums text-muted-foreground">
          {t("taskDetail.chat.roundElapsed", { seconds: (Math.max(0, executionTimeMs) / 1000).toFixed(1) })}
        </span>}
        {description && <p className="w-full break-words text-muted-foreground">{description}</p>}
      </div>
    </div>
    <div className="flex max-w-full shrink-0 flex-wrap items-center gap-2">{actions}</div>
  </div>
}
