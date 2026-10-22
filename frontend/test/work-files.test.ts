import assert from "node:assert/strict";
import test from "node:test";
import { artifactNotice, artifactVersions, resourceVersions } from "../src/lib/work-files.ts";

test("uploaded file version numbers keep their meaning when a new version is added", () => {
  const original = { id: "source", filename: "source.csv", current_version_id: "v2", versions: [{ id: "v2", filename: "source.csv", created_at: "2026-10-17" }, { id: "v1", filename: "source.csv", created_at: "2026-10-16" }] };
  assert.deepEqual(resourceVersions(original).map((version) => [version.id, version.number]), [["v2", 2], ["v1", 1]]);
  const updated = { ...original, current_version_id: "v3", versions: [{ id: "v3", filename: "source.csv" }, ...original.versions] };
  assert.equal(resourceVersions(updated).find((version) => version.id === "v1")?.number, 1);
  assert.equal(resourceVersions(updated)[0].number, 3);
});

test("file versions preserve the authorized server numbering rather than renumbering a published subset", () => {
  assert.deepEqual(artifactVersions([{ id: "shared-v4", version: 4, filename: "report.docx", created_at: "2026-10-17" }]), [{ id: "shared-v4", number: 4, createdAt: "2026-10-17" }]);
  assert.equal(resourceVersions({ id: "source", filename: "source.csv", current_version_id: "only" }).length, 1);
});

test("ordinary successful files have no technical validation badge, while actionable failures remain visible", () => {
  assert.equal(artifactNotice("passed", "ready"), null);
  assert.equal(artifactNotice("passed", "unsupported"), null);
  assert.equal(artifactNotice("pending", "pending"), "preparing");
  assert.equal(artifactNotice("failed", "ready"), "unavailable");
  assert.equal(artifactNotice("needs_review", "failed"), "previewFailed");
  assert.equal(artifactNotice("needs_review", "ready"), "needsReview");
  assert.equal(artifactNotice("passed", "failed"), "previewFailed");
});
