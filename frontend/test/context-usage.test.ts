import assert from "node:assert/strict"
import test from "node:test"
import { mergeContextUsage } from "../src/components/console/task/context-usage.ts"

test("context usage preserves unknown values and does not sum requests", () => {
  const empty = { used: null, size: null }
  const configured = mergeContextUsage(empty, { used: null, size: 128000, updated_at: 0 })
  assert.equal(configured.used, null)
  const first = mergeContextUsage(configured, { used: 12000, size: 128000, estimated: true, updated_at: 100 })
  assert.equal(mergeContextUsage(first, { used: 8000, size: 128000, estimated: true, updated_at: 200 }).used, 8000)
})

test("a reset beats stale stream/history/poll values while retaining capacity", () => {
  const reset = { used: 0, size: 128000, estimated: false, updated_at: 200 }
  assert.deepEqual(mergeContextUsage(reset, { used: 12000, size: 128000, updated_at: 100 }), reset)
  assert.equal(mergeContextUsage(reset, { used: 4000, size: 0, updated_at: 300 }).size, 128000)
  assert.equal(mergeContextUsage(reset, { used: 4000, size: 128000, updated_at: 300 }).used, 4000)
})
