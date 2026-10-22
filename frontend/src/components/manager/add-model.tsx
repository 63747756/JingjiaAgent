import { useState, useEffect, useRef } from "react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Field, FieldContent, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Checkbox } from "@/components/ui/checkbox"
import { apiRequest } from "@/utils/requestUtils"
import { toast } from "sonner"
import type { DomainProviderModelListItem, DomainTeamGroup, DomainTeamModel } from "@/api/Api"
import { ConstsInterfaceType } from "@/api/Api"
import { getModelDisplayName, getModelUrlDescription, modelProviderList } from "@/utils/common"
import { ChevronDown } from "lucide-react"
import { cn } from "@/lib/utils"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { ModelCapabilityFields } from "./model-capability-fields"
import { useTranslation } from "react-i18next"

interface AddModelProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  initialModel?: DomainTeamModel | null
  onRefresh?: () => void
}

const DEFAULT_PROVIDER = "BaiZhiCloud"

export default function AddModel({
  open,
  onOpenChange,
  initialModel,
  onRefresh,
}: AddModelProps) {
  const { t } = useTranslation()
  const [model, setModel] = useState("")
  const [remark, setRemark] = useState("")
  const [apiToken, setApiToken] = useState("")
  const [baseUrl, setBaseUrl] = useState("")
  const [provider, setProvider] = useState(DEFAULT_PROVIDER)
  const [interfaceType, setInterfaceType] = useState<ConstsInterfaceType>(ConstsInterfaceType.InterfaceTypeOpenAIChat)
  const [temperature, setTemperature] = useState<number | undefined>(undefined)
  const [modelList, setModelList] = useState<DomainProviderModelListItem[]>([])
  const [loadingModels, setLoadingModels] = useState(false)
  const [saving, setSaving] = useState(false)
  const [modelListFetchFailed, setModelListFetchFailed] = useState(false)
  const [modelListAttempted, setModelListAttempted] = useState(false)
  const [thinkingEnabled, setThinkingEnabled] = useState(true)
  const [supportImage, setSupportImage] = useState(false)
  const [contextLimit, setContextLimit] = useState("200000")
  const [outputLimit, setOutputLimit] = useState("32000")
  const [selectedGroupIds, setSelectedGroupIds] = useState<string[]>([])
  const [groups, setGroups] = useState<DomainTeamGroup[]>([])
  const [selectOpen, setSelectOpen] = useState(false)
  const selectRef = useRef<HTMLDivElement>(null)

  const resetModelListState = () => {
    setModelList([])
    setModelListAttempted(false)
    setModelListFetchFailed(false)
  }

  const resetForm = (source?: DomainTeamModel | null) => {
    setModel(source?.model || "")
    setRemark(source?.remark || "")
    setApiToken(source?.api_key || "")
    setBaseUrl(source?.base_url || "")
    setProvider(source?.provider || DEFAULT_PROVIDER)
    setInterfaceType(source?.interface_type || ConstsInterfaceType.InterfaceTypeOpenAIChat)
    setTemperature(source?.temperature)
    setThinkingEnabled(source?.thinking_enabled !== false)
    setSupportImage(source?.support_image === true)
    setContextLimit(String(source?.context_limit || 200000))
    setOutputLimit(String(source?.output_limit || 32000))
    resetModelListState()
    setSelectedGroupIds(source?.groups?.map((group) => group.id || "").filter(Boolean) || [])
    setSelectOpen(false)
  }

  const showManualModelInput =
    apiToken.trim() &&
    !loadingModels &&
    modelListAttempted &&
    (modelListFetchFailed || modelList.length === 0)

  useEffect(() => {
    if (open) {
      fetchGroups()
      resetForm(initialModel)
    }
  }, [open, initialModel])

  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (selectRef.current && !selectRef.current.contains(event.target as Node)) {
        setSelectOpen(false)
      }
    }

    if (selectOpen) {
      document.addEventListener("mousedown", handleClickOutside)
    }

    return () => {
      document.removeEventListener("mousedown", handleClickOutside)
    }
  }, [selectOpen])

  const fetchGroups = async () => {
    await apiRequest('v1TeamsGroupsList', {}, [], (resp) => {
      if (resp.code === 0) {
        setGroups(resp.data?.groups || [])
      } else {
        toast.error(t("managerModels.toast.groupFetchFailedWithMessage", { message: resp.message }));
      }
    })
  }

  const handleGroupCheckboxChange = (groupId: string, checked: boolean) => {
    if (checked) {
      setSelectedGroupIds([...selectedGroupIds, groupId])
    } else {
      setSelectedGroupIds(selectedGroupIds.filter(id => id !== groupId))
    }
  }

  const fetchModelList = async () => {
    if (!apiToken.trim()) {
      toast.error(t("managerModels.toast.apiTokenFirst"))
      return
    }

    if (!baseUrl.trim()) {
      toast.error(t("managerModels.toast.baseUrlRequired"))
      return
    }

    setModelListAttempted(true)
    setModelListFetchFailed(false)

    if (modelProviderList[baseUrl.trim()]) {
      setModelList(modelProviderList[baseUrl.trim()])
      return
    }

    setLoadingModels(true)
    await apiRequest('getProviderModelList', {
        api_key: apiToken.trim(),
        base_url: baseUrl.trim(),
        provider,
      }, [], (resp) => {
        if (resp.code === 0) {
          const models = resp.data?.models || []
          setModelList(models)
          setModelListFetchFailed(false)
          if (models.length === 0) {
            toast.warning(t("managerModels.toast.noModelsManual"))
          } else {
            toast.success(t("managerModels.toast.fetchedModels", { count: models.length }))
          }
        } else {
          setModelList([])
          setModelListFetchFailed(true)
          toast.error(t("managerModels.toast.fetchModelsFailedManual", { message: resp.message }))
        }
      })
    setLoadingModels(false)
  }

  const handleSave = async () => {
    if (!apiToken.trim()) {
      toast.error(t("managerModels.toast.apiTokenRequired"))
      return
    }
    if (!baseUrl.trim()) {
      toast.error(t("managerModels.toast.baseUrlRequired"))
      return
    }
    if (!model.trim()) {
      toast.error(t("managerModels.toast.modelRequired"))
      return
    }

    const context = Number(contextLimit)
    const output = Number(outputLimit)
    if (!Number.isSafeInteger(context) || context <= 0 || !Number.isSafeInteger(output) || output <= 0) {
      toast.error(t("managerModels.form.invalidLimits"))
      return
    }
    if (output > context) {
      toast.error(t("managerModels.form.outputExceedsContext"))
      return
    }
    setSaving(true)

    const healthCheckData = {
      api_key: apiToken.trim(),
      model: model.trim(),
      base_url: baseUrl.trim(),
      interface_type: interfaceType,
      provider,
    }

    await apiRequest('v1TeamsModelsHealthCheckCreate', healthCheckData, [], async (resp) => {
      if (resp.code === 0) {
        if (resp.data?.success) {
          const requestData: any = {
            provider,
            model: model.trim(),
            base_url: baseUrl.trim(),
            api_key: apiToken.trim(),
            interface_type: interfaceType,
            thinking_enabled: thinkingEnabled,
            support_image: supportImage,
            context_limit: context,
            output_limit: output,
            group_ids: selectedGroupIds
          }

          if (remark.trim()) {
            requestData.remark = remark.trim()
          }

          if (temperature !== undefined) {
            requestData.temperature = temperature
          }

          await apiRequest('v1TeamsModelsCreate', requestData, [], (resp) => {
            if (resp.code === 0) {
              toast.success(t("managerModels.toast.bindSuccess"))
              resetForm()
              onOpenChange(false)
              onRefresh?.()
            } else {
              toast.error(t("managerModels.toast.bindFailedWithMessage", { message: resp.message }));
            }
          })
        } else {
          toast.error(t("managerModels.toast.configCheckFailedWithMessage", { message: resp.data?.error }))
        }
      }
    })
    
    setSaving(false)
  }

  const handleCancel = () => {
    resetForm()
    onOpenChange(false)
  }

  // Group and sort provider model options.
  const getGroupedModels = () => {
    const groups: Record<string, DomainProviderModelListItem[]> = {}
    
    modelList.forEach((item) => {
      const modelName = item.model || ""
      const parts = modelName.split("-")
      const groupKey = parts.length > 0 ? parts[0] : t("managerModels.fallback.otherGroup")
      
      if (!groups[groupKey]) {
        groups[groupKey] = []
      }
      groups[groupKey].push(item)
    })
    
    // Sort models inside each group by name.
    Object.keys(groups).forEach((key) => {
      groups[key].sort((a, b) => {
        const aName = a.model || ""
        const bName = b.model || ""
        return aName.localeCompare(bName)
      })
    })
    
    // Sort group names.
    const sortedGroupKeys = Object.keys(groups).sort()
    
    return { groups, sortedGroupKeys }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (!nextOpen) {
          resetForm()
        }
        onOpenChange(nextOpen)
      }}
    >
      <DialogTrigger asChild>
        <Button variant={"outline"} size="sm">{t("managerModels.actions.bind")}</Button>
      </DialogTrigger>
      <DialogContent className="flex max-h-[90vh] flex-col overflow-hidden">
        <DialogHeader>
          <DialogTitle>{t("managerModels.form.addTitle")}</DialogTitle>
        </DialogHeader>
        <div className="grid min-h-0 flex-1 gap-4 overflow-y-auto overscroll-contain pr-1">
          <Field>
            <FieldLabel>{t("managerModels.form.interfaceFormat")}</FieldLabel>
            <FieldContent>
              <Select
                value={interfaceType}
                onValueChange={(value) => setInterfaceType(value as ConstsInterfaceType)}
              >
                <SelectTrigger className="w-full">
                  <SelectValue placeholder={t("managerModels.form.interfacePlaceholder")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ConstsInterfaceType.InterfaceTypeOpenAIResponse}>
                    OpenAI Responses
                  </SelectItem>
                  <SelectItem value={ConstsInterfaceType.InterfaceTypeOpenAIChat}>
                    OpenAI Chat
                  </SelectItem>
                  <SelectItem value={ConstsInterfaceType.InterfaceTypeAnthropic}>
                    Anthropic
                  </SelectItem>
                </SelectContent>
              </Select>
            </FieldContent>
          </Field>
          <Field>
            <FieldLabel>{t("managerModels.form.modelApiUrl")}</FieldLabel>
            <FieldContent>
              <Input
                placeholder={t("managerModels.form.modelApiUrlPlaceholder")}
                value={baseUrl}
                onChange={(e) => {
                  setBaseUrl(e.target.value)
                  resetModelListState()
                }}
              />
            </FieldContent>
            <FieldDescription>{getModelUrlDescription(baseUrl, interfaceType)}</FieldDescription>
          </Field>
          <Field>
            <div className="flex items-center justify-between gap-2">
              <FieldLabel>API Token</FieldLabel>
              <Button
                type="button"
                variant="link"
                size="sm"
                asChild
                className="h-auto p-0 text-foreground"
              >

              </Button>
            </div>
            <FieldContent>
              <Input
                placeholder={t("managerModels.form.apiTokenPlaceholder")}
                value={apiToken}
                onChange={(e) => {
                  setApiToken(e.target.value)
                  resetModelListState()
                }}
              />
            </FieldContent>
          </Field>
          <Field>
            <FieldLabel>{t("managerModels.form.modelName")}</FieldLabel>
            <FieldContent>
              {showManualModelInput ? (
                <>
                  <Input
                    placeholder={t("managerModels.form.modelNamePlaceholder")}
                    value={model}
                    onChange={(e) => setModel(e.target.value)}
                  />
                  <FieldDescription>
                    {modelListFetchFailed
                      ? t("managerModels.form.fetchFailedDescription")
                      : t("managerModels.form.emptyModelsDescription")}
                  </FieldDescription>
                </>
              ) : (
                <Select
                  value={model}
                  onValueChange={setModel}
                  onOpenChange={(open) => {
                    if (open && apiToken.trim() && !loadingModels) {
                      fetchModelList()
                    }
                  }}
                  disabled={loadingModels || !apiToken.trim()}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue
                      placeholder={loadingModels ? t("managerModels.form.loading") : model || t("managerModels.form.selectModel")}
                    />
                  </SelectTrigger>
                  <SelectContent>
                    {loadingModels ? (
                      <div className="flex items-center justify-center py-4">
                        <Spinner />
                        <span className="ml-2 text-sm text-muted-foreground">
                          {t("managerModels.form.loadingModels")}
                        </span>
                      </div>
                    ) : modelList.length > 0 ? (() => {
                      const { groups, sortedGroupKeys } = getGroupedModels()
                      return (
                        <>
                          {sortedGroupKeys.map((groupKey) => (
                            <SelectGroup key={groupKey}>
                              <SelectLabel>{groupKey}</SelectLabel>
                              {groups[groupKey].map((item, index) => (
                                <SelectItem key={`${groupKey}-${index}`} value={item.model || ""}>
                                  {getModelDisplayName(item.model)}
                                </SelectItem>
                              ))}
                            </SelectGroup>
                          ))}
                        </>
                      )
                    })() : model ? (
                      <SelectItem value={model}>
                        {model}
                      </SelectItem>
                    ) : (
                      <div className="py-4 text-center text-sm text-muted-foreground">
                        {apiToken.trim()
                          ? t("managerModels.form.noModels")
                          : t("managerModels.toast.apiTokenFirst")}
                      </div>
                    )}
                  </SelectContent>
                </Select>
              )}
            </FieldContent>
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field>
              <FieldLabel htmlFor="team-model-context">{t("managerModels.form.contextLimit")}</FieldLabel>
              <FieldContent><Input id="team-model-context" type="number" min={1} step={1} value={contextLimit} onChange={e => setContextLimit(e.target.value)} disabled={saving} /></FieldContent>
            </Field>
            <Field>
              <FieldLabel htmlFor="team-model-output">{t("managerModels.form.outputLimit")}</FieldLabel>
              <FieldContent><Input id="team-model-output" type="number" min={1} step={1} value={outputLimit} onChange={e => setOutputLimit(e.target.value)} disabled={saving} /></FieldContent>
            </Field>
          </div>
          <FieldDescription>{t("managerModels.form.limitsDescription")}</FieldDescription>
          <ModelCapabilityFields
            thinkingEnabled={thinkingEnabled}
            onThinkingChange={setThinkingEnabled}
            supportImage={supportImage}
            onImageChange={setSupportImage}
            disabled={saving}
          />
          <Field>
            <FieldLabel>{t("managerModels.form.groupsLabel")}</FieldLabel>
            <FieldContent>
              <div className="relative" ref={selectRef}>
                <Button
                  type="button"
                  variant="outline"
                  role="combobox"
                  aria-expanded={selectOpen}
                  className="w-full justify-between"
                  onClick={() => setSelectOpen(!selectOpen)}
                >
                  <span className="truncate">
                    {selectedGroupIds.length === 0
                      ? t("managerModels.form.selectGroups")
                      : selectedGroupIds.length === 1
                      ? groups.find((g) => g.id === selectedGroupIds[0])?.name || t("managerModels.form.selectedOne")
                      : t("managerModels.form.selectedMany", { count: selectedGroupIds.length })}
                  </span>
                  <ChevronDown className={cn("ml-2 h-4 w-4 shrink-0 opacity-50 transition-transform", selectOpen && "rotate-180")} />
                </Button>
                {selectOpen && (
                  <div className="absolute z-50 mt-1 w-full rounded-md border bg-popover shadow-md">
                    <div className="max-h-[300px] overflow-auto p-1">
                      {groups.length === 0 ? (
                        <div className="py-6 text-center text-sm text-muted-foreground">
                          {t("managerModels.fallback.noGroups")}
                        </div>
                      ) : (
                        groups.map((group) => {
                          const isChecked = selectedGroupIds.includes(group.id || "")
                          return (
                            <div
                              key={group.id}
                              className="flex items-center gap-2 rounded-sm px-2 py-1.5 hover:bg-accent cursor-pointer"
                              onClick={() => handleGroupCheckboxChange(group.id || "", !isChecked)}
                            >
                              <Checkbox
                                checked={isChecked}
                                onCheckedChange={(checked) => handleGroupCheckboxChange(group.id || "", checked as boolean)}
                                onClick={(e) => e.stopPropagation()}
                              />
                              <span className="text-sm">{group.name}</span>
                            </div>
                          )
                        })
                      )}
                    </div>
                  </div>
                )}
              </div>
            </FieldContent>
          </Field>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={handleCancel} disabled={saving}>
            {t("managerModels.actions.cancel")}
          </Button>
          <Button onClick={handleSave} disabled={!model.trim() || saving}>
            {saving && <Spinner className="size-4" />}
            {t("managerModels.actions.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
