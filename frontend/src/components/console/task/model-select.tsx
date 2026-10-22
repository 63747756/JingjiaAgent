import type { DomainModel, DomainSubscriptionResp } from "@/api/Api"
import { ConstsOwnerType } from "@/api/Api"
import Icon from "@/components/common/Icon"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  getBrandFromModel,
  getBuiltinModelName,
  getModelDisplayName,
  getOwnerTypeBadge,
  isBuiltinPublicModelPackage,
  stripBuiltinPublicModelPackagePrefix,
  canUseModelBySubscription,
} from "@/utils/common"
import { IS_OFFLINE_EDITION } from "@/utils/edition"
import { useIsMobile } from "@/hooks/use-mobile"
import { cn } from "@/lib/utils"
import { IconChevronDown } from "@tabler/icons-react"
import { useCallback, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"

const BUILTIN_MODEL_OPTIONS = [
  {
    model: "jingjiaagent-basic",
  },
  {
    model: "jingjiaagent-pro",
  },
  {
    model: "jingjiaagent-ultra",
  },
] as const

type BuiltinModelName = typeof BUILTIN_MODEL_OPTIONS[number]["model"]

interface ModelSelectProps {
  models: DomainModel[]
  selectedModel?: DomainModel
  selectedModelId: string
  setSelectedModelId: (modelId: string) => void
  className?: string
  subscription?: DomainSubscriptionResp | null
  compact?: boolean
  disabled?: boolean
  loading?: boolean
  error?: string
}

export default function ModelSelect({
  models,
  selectedModel,
  selectedModelId,
  setSelectedModelId,
  className,
  subscription,
  compact = false,
  disabled = false,
  loading = false,
  error,
}: ModelSelectProps) {
  const { t } = useTranslation()
  const isMobile = useIsMobile()
  const [query, setQuery] = useState("")
  const getBuiltinModelLabel = useCallback((modelName?: string | null) => {
    if (modelName === "jingjiaagent-basic") {
      return t("taskWorkflow.model.basic")
    }
    if (modelName === "jingjiaagent-pro") {
      return t("taskWorkflow.model.pro")
    }
    if (modelName === "jingjiaagent-ultra") {
      return t("taskWorkflow.model.ultra")
    }
    return ""
  }, [t])
  const getBuiltinModelBadge = useCallback((modelName?: string | null) => {
    if (modelName === "jingjiaagent-basic") {
      return t("taskWorkflow.model.freeUsage")
    }
    if (modelName === "jingjiaagent-pro") {
      return t("taskWorkflow.model.proUsage")
    }
    if (modelName === "jingjiaagent-ultra") {
      return t("taskWorkflow.model.ultraUsage")
    }
    return undefined
  }, [t])
  const supportedModels = useMemo(
    () => models.filter((model) => (model.id || model.model) && `${model.remark || ""} ${model.model || ""} ${model.owner?.name || ""}`.toLocaleLowerCase().includes(query.toLocaleLowerCase())),
    [models, query],
  )
  const recommendedModelKeys = useMemo(() => {
    return BUILTIN_MODEL_OPTIONS.reduce((recommended, option) => {
      const recommendedModel = supportedModels
        .filter((model) => getBuiltinModelName(model.model) === option.model)
        .sort((left, right) => {
          const weightDiff = (right.weight || 0) - (left.weight || 0)
          if (weightDiff !== 0) {
            return weightDiff
          }

          const nameDiff = (left.model || "").localeCompare(right.model || "")
          if (nameDiff !== 0) {
            return nameDiff
          }

          return (left.id || "").localeCompare(right.id || "")
        })[0]

      const recommendedKey = recommendedModel?.id || recommendedModel?.model
      if (recommendedKey) {
        recommended[option.model] = recommendedKey
      }

      return recommended
    }, {} as Partial<Record<BuiltinModelName, string>>)
  }, [supportedModels])
  const builtinModelGroups = useMemo(
    () => IS_OFFLINE_EDITION
      ? []
      : BUILTIN_MODEL_OPTIONS.map((option) => ({
        key: option.model,
        label: getBuiltinModelLabel(option.model),
        badge: getBuiltinModelBadge(option.model),
        badgeVariant: option.model === "jingjiaagent-basic" ? "default" as const : "secondary" as const,
        iconName: option.model === "jingjiaagent-basic"
          ? "gift"
          : option.model === "jingjiaagent-pro"
            ? "vip-1"
            : "vip-2",
        models: supportedModels.filter((model) => getBuiltinModelName(model.model) === option.model),
      })),
    [supportedModels, getBuiltinModelLabel, getBuiltinModelBadge],
  )
  const privateModels = useMemo(
    () => supportedModels.filter((model) => (
      model.owner?.type === ConstsOwnerType.OwnerTypePrivate
      && !getBuiltinModelName(model.model)
    )),
    [supportedModels],
  )
  const otherPaidModels = useMemo(
    () => supportedModels.filter((model) => (
      model.owner?.type === ConstsOwnerType.OwnerTypePublic
      && !isBuiltinPublicModelPackage(model.model)
    )),
    [supportedModels],
  )
  const teamModelGroups = useMemo(
    () => Array.from(
      supportedModels
        .filter((model) => (
          model.owner?.type === ConstsOwnerType.OwnerTypeTeam
          && !getBuiltinModelName(model.model)
        ))
        .reduce((groups, model) => {
          const teamName = model.owner?.name || t("taskWorkflow.model.team")
          const teamId = model.owner?.id || teamName
          const groupKey = `${teamId}:${teamName}`
          const group = groups.get(groupKey) || { key: groupKey, label: teamName, iconName: "team", models: [] as DomainModel[] }
          group.models.push(model)
          groups.set(groupKey, group)
          return groups
        }, new Map<string, { key: string; label: string; iconName: string; models: DomainModel[] }>())
        .values(),
    ),
    [supportedModels, t],
  )
  const modelGroups = useMemo(
    () => [
      ...builtinModelGroups,
      {
        key: "other-public-models",
        label: t("taskWorkflow.model.paid"),
        badge: t("taskWorkflow.model.paidUsage"),
        iconName: "qiandaizi",
        models: otherPaidModels,
      },
      {
        key: "private-models",
        label: t("taskWorkflow.model.mine"),
        iconName: "a-AIshezhi",
        models: privateModels,
      },
      ...teamModelGroups,
    ].filter((group) => group.models.length > 0),
    [builtinModelGroups, privateModels, otherPaidModels, teamModelGroups, t],
  )
  const hasSelectableModel = modelGroups.some((group) => group.models.some((model) => model.id))
  const [openModelGroupKey, setOpenModelGroupKey] = useState<string>()

  const getNestedModelDisplayName = (modelName?: string | null) => {
    const normalizedModelName = modelName?.trim()
    if (!normalizedModelName) {
      return ""
    }

    const builtinModelName = getBuiltinModelName(normalizedModelName)
    if (!builtinModelName) {
      return getModelDisplayName(normalizedModelName)
    }

    const nestedModelName = normalizedModelName.slice(builtinModelName.length).replace(/^\/+/, "")
    return nestedModelName || getModelDisplayName(normalizedModelName)
  }

  const getModelOptionDisplayName = (model: DomainModel, nested = false) => {
    const remark = model.remark?.trim()
    if (remark) {
      return stripBuiltinPublicModelPackagePrefix(remark)
    }

    return nested ? getNestedModelDisplayName(model.model) : getModelDisplayName(model.model)
  }

  const getSelectedModelDisplayName = () => {
    const builtinModelName = getBuiltinModelName(selectedModel?.model)
    const builtinOption = BUILTIN_MODEL_OPTIONS.find((option) => option.model === builtinModelName)
    if (builtinOption && selectedModel) {
      const nestedModelName = getModelOptionDisplayName(selectedModel, true)
      return isMobile ? nestedModelName : `${getBuiltinModelLabel(builtinOption.model)} / ${nestedModelName}`
    }

    return selectedModel ? getModelOptionDisplayName(selectedModel) : ""
  }

  const getRecommendedModelBadge = (model: DomainModel) => {
    const builtinModelName = getBuiltinModelName(model.model)
    if (!builtinModelName) {
      return null
    }

    const modelKey = model.id || model.model
    if (modelKey && recommendedModelKeys[builtinModelName] === modelKey) {
      return t("taskWorkflow.model.recommended")
    }

    return null
  }

  const renderModelOption = (model: DomainModel, nested = false, indented = false) => {
    const displayName = getModelOptionDisplayName(model, nested)
    const recommendedBadge = getRecommendedModelBadge(model)

    return (
      <DropdownMenuRadioItem
        key={model.id || model.model}
        value={model.id || ""}
        className={cn(
          "w-full justify-between gap-3 pr-2 [&>[data-slot=dropdown-menu-radio-item-indicator]]:hidden",
          indented && "pl-7",
        )}
        disabled={!model.id || !canUseModelBySubscription(model, subscription)}
      >
        <div className="flex min-w-0 flex-1 items-center gap-2">
          <Icon name={getBrandFromModel(model)} className="size-4" />
          <span className="truncate">{displayName}</span>
        </div>
        <div className="ml-auto flex shrink-0 items-center justify-end gap-1.5">
          {recommendedBadge ? (
            <Badge variant="secondary" className="shrink-0">{recommendedBadge}</Badge>
          ) : null}
          {model.owner?.type !== ConstsOwnerType.OwnerTypePublic && getOwnerTypeBadge(model.owner)}
        </div>
      </DropdownMenuRadioItem>
    )
  }

  const renderModelGroupHeader = (group: { key: string; label: string; badge?: string; badgeVariant?: "default" | "secondary"; iconName?: string; models: DomainModel[] }) => (
    <div key={group.key} className="flex min-w-0 items-center gap-2 px-2 py-1.5 text-xs font-medium text-muted-foreground">
      {group.iconName ? (
        <Icon name={group.iconName} className="size-4 shrink-0" />
      ) : null}
      <span className="truncate">{group.label}</span>
      {group.badge ? (
        <Badge
          variant={group.badgeVariant || "secondary"}
          className={cn("shrink-0", group.badgeVariant === "default" && "!text-primary-foreground")}
        >
          {group.badge}
        </Badge>
      ) : null}
    </div>
  )

  const renderModelGroup = (group: { key: string; label: string; badge?: string; badgeVariant?: "default" | "secondary"; iconName?: string; models: DomainModel[] }) => {
    const hasAvailableModel = group.models.some((model) => model.id)

    return (
      <DropdownMenuSub
        key={group.key}
        open={openModelGroupKey === group.key}
        onOpenChange={(open) => {
          setOpenModelGroupKey((currentKey) => {
            if (open) {
              return group.key
            }

            return currentKey === group.key ? undefined : currentKey
          })
        }}
      >
        <DropdownMenuSubTrigger className="w-full" disabled={!hasAvailableModel}>
          <span className="flex min-w-0 flex-1 items-center gap-2">
            {group.iconName ? (
              <Icon name={group.iconName} className="size-4 shrink-0" />
            ) : null}
            <span className="truncate">{group.label}</span>
            {group.badge ? (
              <Badge
                variant={group.badgeVariant || "secondary"}
                className={cn("shrink-0", group.badgeVariant === "default" && "!text-primary-foreground")}
              >
                {group.badge}
              </Badge>
            ) : null}
          </span>
        </DropdownMenuSubTrigger>
        <DropdownMenuSubContent className="max-h-[320px] min-w-[280px] overflow-y-auto">
          {group.models.map((model) => renderModelOption(model, Boolean(getBuiltinModelName(model.model))))}
        </DropdownMenuSubContent>
      </DropdownMenuSub>
    )
  }

  return (
    <div className={cn(compact ? "min-w-0 max-w-[min(12rem,30vw)] shrink" : "w-full", className)}>
      <div className="flex w-full items-center gap-2">
        <DropdownMenu onOpenChange={(open) => {
          if (!open) {
            setOpenModelGroupKey(undefined)
            setQuery("")
          }
        }}>
          <DropdownMenuTrigger asChild>
            <Button
              size="sm"
              variant={compact ? "ghost" : "outline"}
              aria-label={t("taskWorkflow.model.select")}
              title={getSelectedModelDisplayName()}
              className={cn("h-8 min-w-0 flex-1 justify-between rounded-md px-2 text-sm", compact && "rounded-lg px-1.5 font-normal max-sm:h-11 max-sm:text-xs")}
              disabled={disabled || loading || (!compact && !hasSelectableModel)}
            >
              {selectedModel ? (
                <span className="flex min-w-0 flex-1 items-center gap-2">
                  <Icon name={getBrandFromModel(selectedModel)} className="size-4 shrink-0" />
                  <span className="truncate">{getSelectedModelDisplayName()}</span>
                </span>
              ) : (
                <span className="truncate text-muted-foreground">{t("taskWorkflow.model.select")}</span>
              )}
              <IconChevronDown className="size-3.5 shrink-0 text-muted-foreground" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" side={compact ? "top" : undefined} className="max-h-[min(420px,var(--radix-dropdown-menu-content-available-height))] min-w-[320px] overflow-y-auto max-sm:w-[calc(100vw-2rem)] max-sm:min-w-0">
            {compact && <Input aria-label={t("taskWorkflow.model.search")} placeholder={t("taskWorkflow.model.search")} value={query} onChange={e => setQuery(e.target.value)} onKeyDown={e => e.stopPropagation()} className="mb-1 h-8" />}
            {error ? <p role="alert" className="px-2 py-2 text-xs text-destructive">{error}</p> : !hasSelectableModel && <p className="px-2 py-2 text-xs text-muted-foreground">{t("taskDetail.page.models.empty")}</p>}
            <DropdownMenuRadioGroup value={selectedModelId} onValueChange={setSelectedModelId}>
              {isMobile || compact ? modelGroups.map((group) => (
                <div key={group.key} className="not-first:mt-1 not-first:border-t not-first:border-border not-first:pt-2">
                  {renderModelGroupHeader(group)}
                  <div className="mt-1 space-y-0.5">
                    {group.models.map((model) => renderModelOption(model, Boolean(getBuiltinModelName(model.model)), true))}
                  </div>
                </div>
              )) : modelGroups.map(renderModelGroup)}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </div>
  )
}
