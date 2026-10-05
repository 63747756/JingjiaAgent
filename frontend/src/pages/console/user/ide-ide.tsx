import {
  BookOpenIcon,
  CalendarDays,
  ExternalLink
} from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { useTranslation } from "react-i18next"

export default function IDEIDE() {
  const { t } = useTranslation()

  return (
    <Empty className="bg-muted">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <CalendarDays />
        </EmptyMedia>
        <EmptyTitle>{t("consoleIde.comingSoonTitle")}</EmptyTitle>
        <EmptyDescription>
          {t("consoleIde.comingSoonDescription")}
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <div className="flex gap-2">
          <Button asChild variant="outline">
            <a href="https://github.com/63747756/JingjiaAgent" target="_blank">
              <ExternalLink />
              {t("consoleIde.openSourceRepository")}
            </a>
          </Button>
          <Button>
            <BookOpenIcon />

          </Button>
        </div>
      </EmptyContent>
    </Empty>
  )
}
