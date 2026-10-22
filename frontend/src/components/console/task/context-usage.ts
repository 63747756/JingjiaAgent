export type ContextUsage = {
  size: number | null
  used: number | null
  estimated?: boolean
  updated_at?: number
}

export function mergeContextUsage(previous: ContextUsage, next?: ContextUsage): ContextUsage {
  if (!next || (next.updated_at ?? 0) < (previous.updated_at ?? 0)) return previous
  return {
    size: next.size && next.size > 0 ? next.size : previous.size,
    used: next.used ?? previous.used,
    estimated: next.estimated ?? previous.estimated,
    updated_at: next.updated_at ?? previous.updated_at,
  }
}
