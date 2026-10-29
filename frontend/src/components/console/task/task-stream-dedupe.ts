export interface TaskStreamDedupChunk {
  type?: string
  kind?: string
  data?: unknown
  timestamp?: number
  seq?: number | string
  turn_seq?: number
}

export function getTaskStreamDedupKey(chunk: TaskStreamDedupChunk) {
  return JSON.stringify([
    chunk.type ?? "",
    chunk.kind ?? "",
    chunk.turn_seq ?? "",
    chunk.seq ?? "",
    chunk.timestamp ?? 0,
    typeof chunk.data === "string" ? chunk.data : chunk.data == null ? "" : JSON.stringify(chunk.data),
  ])
}
