export type ContinuationStatus = { id: string; message_id: string; state: string; phase: string; error?: string; checkpoint_id?: string }
export type StorageIssue = { id: string; kind: string; error: string; attempts: number; paused: boolean }

export function isPendingContinuation(item: ContinuationStatus) {
  return item.state === "waiting" || item.state === "blocked" || item.state === "pending"
}

export function selectWorkTaskStatus(items: ContinuationStatus[], issues: StorageIssue[], messageId: string | undefined, active: boolean) {
  const currentIndex = messageId ? items.findIndex(item => item.message_id === messageId) : 0
  let continuation: ContinuationStatus | undefined = items[currentIndex]
  // A queued message cannot proceed until an earlier blocked turn is recovered.
  if (continuation?.phase === "queued" && isPendingContinuation(continuation)) {
    continuation = items.slice(0, currentIndex).find(item => item.state === "blocked") ?? continuation
  } else if (!continuation && !active) {
    continuation = items.find(item => item.state === "blocked")
  }
  const storage = issues.find(issue => issue.kind === "checkpoint") ?? issues[0]
  // A save failure is the cause of a saving continuation, not another banner.
  const kind = storage && (!continuation || continuation.phase === "saving") ? "storage"
    : continuation?.state === "blocked" ? "blocked"
    : continuation?.state === "running" ? "running"
    : continuation || active ? "preparing"
    : "idle"
  return { kind, continuation, storage }
}
