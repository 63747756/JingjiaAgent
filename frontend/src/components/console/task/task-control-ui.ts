import type { DomainModel, DomainProjectTask } from "@/api/Api"
import type { TaskControlCallResponse, TaskControlClientState, TaskControlOperation } from "./task-control-client"

export function taskControlNoticeKey(state: TaskControlClientState) {
  if (!state.operation) return null
  if (state.status !== "connected") return "taskDetail.page.control.reconnecting"
  if (state.operation.status === "uncertain") return "taskDetail.page.control.uncertain"
  if (state.operation.status === "pending") return "taskDetail.page.control.pending"
  return "taskDetail.page.control.waiting"
}

export function isSuccessfulControlResult(response: TaskControlCallResponse | null | undefined) {
  return response?.success === true && (response.status === undefined || response.status === "succeeded")
}

// Only an acknowledged terminal result may apply a requested selection locally.
export function applyTaskControlResult(
  task: DomainProjectTask | null,
  operation: TaskControlOperation,
  response: TaskControlCallResponse,
  models: DomainModel[],
): DomainProjectTask | null {
  if (!task || !isSuccessfulControlResult(response)) return task
  if (operation.kind === "switch_model") {
    const selectedModel = models.find((candidate) => candidate.id === operation.payload.model_id)
      ?? (task.model?.id === operation.payload.model_id ? task.model : undefined)
    const returnedModel = response.model && typeof response.model === "object" && !Array.isArray(response.model)
      ? response.model as DomainModel
      : undefined
    // ID-only recovery responses may serialize empty strings and zero defaults.
    const model = returnedModel?.model
      ? { ...selectedModel, ...returnedModel, provider: returnedModel.provider || selectedModel?.provider }
      : selectedModel ?? returnedModel
    return model ? { ...task, model } : task
  }
  if (operation.kind === "switch_agent_resources") {
    const { skill_ids, plugin_ids } = operation.payload
    if (!Array.isArray(skill_ids) || !skill_ids.every((id) => typeof id === "string")
      || !Array.isArray(plugin_ids) || !plugin_ids.every((id) => typeof id === "string")) return task
    return { ...task, extra: { ...task.extra, skill_ids: [...skill_ids], plugin_ids: [...plugin_ids] } }
  }
  return task
}

export async function repairTaskError({ reload, send, isCurrent }: {
  reload: () => Promise<TaskControlCallResponse | null> | undefined
  send: () => Promise<boolean> | boolean | undefined
  isCurrent: () => boolean
}): Promise<"succeeded" | "failed" | "detached"> {
  const response = await reload()
  if (!isCurrent() || response?.status === "detached") return "detached"
  if (!isSuccessfulControlResult(response)) return "failed"
  const sent = await send()
  if (!isCurrent()) return "detached"
  return sent ? "succeeded" : "failed"
}
