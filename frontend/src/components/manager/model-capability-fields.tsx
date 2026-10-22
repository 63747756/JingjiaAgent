import { useId } from "react"
import { useTranslation } from "react-i18next"
import { Field, FieldContent, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Switch } from "@/components/ui/switch"

interface ModelCapabilityFieldsProps {
  thinkingEnabled: boolean
  onThinkingChange: (enabled: boolean) => void
  supportImage: boolean
  onImageChange: (enabled: boolean) => void
  disabled?: boolean
}

export function ModelCapabilityFields({
  thinkingEnabled, onThinkingChange, supportImage, onImageChange, disabled,
}: ModelCapabilityFieldsProps) {
  const { t } = useTranslation()
  const id = useId()
  return (
    <div className="grid gap-4" data-slot="model-capabilities">
      <Field>
        <FieldLabel htmlFor={`${id}-thinking`}>{t("consoleSettings.models.labels.thinking")}</FieldLabel>
        <FieldContent>
          <div className="flex items-center justify-between rounded-md border px-3 py-2">
            <span className="text-sm text-muted-foreground">
              {thinkingEnabled ? t("consoleSettings.models.status.enabled") : t("consoleSettings.models.status.disabled")}
            </span>
            <Switch id={`${id}-thinking`} checked={thinkingEnabled} onCheckedChange={onThinkingChange}
              disabled={disabled} />
          </div>
        </FieldContent>
      </Field>
      <Field>
        <FieldLabel htmlFor={`${id}-image`}>{t("consoleSettings.models.labels.imageRecognition")}</FieldLabel>
        <FieldContent>
          <div className="flex items-center justify-between rounded-md border px-3 py-2">
            <span className="text-sm text-muted-foreground">
              {supportImage ? t("consoleSettings.models.status.supported") : t("consoleSettings.models.status.unsupported")}
            </span>
            <Switch id={`${id}-image`} checked={supportImage} onCheckedChange={onImageChange}
              disabled={disabled} aria-describedby={`${id}-image-description`} />
          </div>
        </FieldContent>
        <FieldDescription id={`${id}-image-description`}>{t("consoleSettings.models.descriptionText.imageRecognition")}</FieldDescription>
      </Field>
    </div>
  )
}
