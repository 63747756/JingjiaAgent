import { useEffect, useRef, useState } from "react"
import type { MessageType } from "./message"
import { repairTaskError } from "./task-control-ui"
import { IconAlertTriangle, IconReload } from "@tabler/icons-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { HoverCard, HoverCardContent, HoverCardTrigger } from "@/components/ui/hover-card"
import { Label } from "@/components/ui/label"
import { toast } from "sonner"
import { useTranslation } from "react-i18next"

export const ErrorMessageItem = ({ message }: { message: MessageType }) => {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [repairing, setRepairing] = useState(false)
  const repairingRef = useRef(false)
  const mountedRef = useRef(false)
  useEffect(() => {
    mountedRef.current = true
    return () => { mountedRef.current = false }
  }, [])

  return (
    <HoverCard open={open} onOpenChange={setOpen} openDelay={100} closeDelay={200}>
      <HoverCardTrigger asChild>
        <Badge variant="destructive" className="max-w-[80%] cursor-pointer">
          <IconAlertTriangle className="size-4" />
          <div className="min-w-0 flex-1 whitespace-normal line-clamp-1 break-all">
            {message.data.details}
          </div>
        </Badge>
      </HoverCardTrigger>
      <HoverCardContent className="max-w-[500px] w-auto p-4 flex flex-col gap-4" side="bottom" align="start">
        <Label>{t("taskDetail.error.details")}</Label>

        <pre className="bg-muted px-3 py-2 rounded-md whitespace-pre-wrap break-all text-xs overflow-y-auto max-h-[70vh]">
          {message.data.details}
        </pre>

        <div className="flex flex-row gap-2 items-center">
          <p className="text-sm text-muted-foreground flex-1">{t("taskDetail.error.repairTip")}</p>
          <Button
            variant="default"
            size="sm"
            className="cursor-pointer"
            disabled={repairing || message.controlBusy}
            onClick={async () => {
              if (repairingRef.current || message.controlBusy) return
              repairingRef.current = true
              setRepairing(true)
              try {
                let failureMessage: string | undefined
                const result = await repairTaskError({
                  reload: async () => {
                    const response = await message.onReloadSession?.()
                    failureMessage = response?.error || response?.message
                    return response ?? null
                  },
                  send: () => message.onUserInput?.(t("taskDetail.error.continueTask")),
                  isCurrent: () => mountedRef.current,
                })
                if (result === "detached") return
                if (result === "failed") toast.error(failureMessage || t("taskDetail.error.repairFailed"))
                else setOpen(false)
              } catch {
                if (mountedRef.current) toast.error(t("taskDetail.error.repairFailed"))
              } finally {
                repairingRef.current = false
                if (mountedRef.current) setRepairing(false)
              }
            }}
          >
            <IconReload className={repairing ? "size-3 mr-1 animate-spin" : "size-3 mr-1"} />
            {repairing ? t("taskDetail.error.repairing") : t("taskDetail.error.repair")}
          </Button>
        </div>
      </HoverCardContent>
    </HoverCard>
  )
}
