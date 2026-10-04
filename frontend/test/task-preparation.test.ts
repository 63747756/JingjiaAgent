import assert from "node:assert/strict"
import test from "node:test"
import { taskPreparationStage } from "../src/utils/task-preparation.ts"
import type { DomainProjectTask } from "../src/api/Api.ts"
import cn from "../src/i18n/resources/cn.ts"
import en from "../src/i18n/resources/en.ts"

test("pending tasks have a waiting label before the first condition arrives", () => {
  assert.equal(taskPreparationStage({ status: "pending" } as DomainProjectTask), "waiting")
  assert.equal(taskPreparationStage({ status: "pending", virtualmachine: { conditions: [] } } as DomainProjectTask), "waiting")
  assert.equal(taskPreparationStage(null), undefined)
})

test("runtime facts select translated preparation messages without guessing detailed progress", () => {
  const stages = {
    RuntimePreparationWaiting: "waiting", RuntimePreparing: "preparing",
    RuntimePreparationReconciling: "reconciling", RuntimePreparationCanceling: "canceling",
    RuntimePrepared: "ready", RuntimePreparationFailed: "failed", RuntimePreparationCanceled: "canceled",
  } as const
  for (const [reason, stage] of Object.entries(stages)) {
    assert.equal(taskPreparationStage({ status: "pending", virtualmachine: { conditions: [{ type: "Scheduled", reason }] } } as DomainProjectTask), stage)
    for (const language of [cn, en]) {
      assert.ok(language.taskDetail.preparing[stage].title)
      assert.ok(language.taskDetail.preparing[stage].detail)
    }
  }
})

test("legacy detailed stages and normal task views retain their existing behavior", () => {
  assert.equal(taskPreparationStage({ status: "pending", virtualmachine: { conditions: [{ type: "ImagePulled", message: "Downloading image" }] } } as DomainProjectTask), undefined)
  assert.equal(taskPreparationStage({ status: "finished" } as DomainProjectTask), undefined)
  assert.equal(taskPreparationStage({ status: "pending", virtualmachine: { conditions: [{ type: "Scheduled", reason: "toString" }] } } as DomainProjectTask), undefined)
})
