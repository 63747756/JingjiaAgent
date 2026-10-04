import type { DomainProjectTask } from "../api/Api"

export type TaskPreparationStage = "waiting" | "preparing" | "reconciling" | "canceling" | "ready" | "failed" | "canceled"

const runtimeStages: Record<string, TaskPreparationStage> = {
  RuntimePreparationWaiting: "waiting",
  RuntimePreparing: "preparing",
  RuntimePreparationReconciling: "reconciling",
  RuntimePreparationCanceling: "canceling",
  RuntimePrepared: "ready",
  RuntimePreparationFailed: "failed",
  RuntimePreparationCanceled: "canceled",
}

export function taskPreparationStage(task: DomainProjectTask | null): TaskPreparationStage | undefined {
  const conditions = task?.virtualmachine?.conditions
  const last = conditions?.[conditions.length - 1]
  if (last?.reason && Object.hasOwn(runtimeStages, last.reason)) return runtimeStages[last.reason]
  // The first response can precede condition availability. Retain legacy
  // detailed stages, while avoiding an unknown heading on a new pending task.
  if (!last?.type) {
    if (task?.status === "pending") return "waiting"
    if (task?.status === "error") return "failed"
  }
  return undefined
}
