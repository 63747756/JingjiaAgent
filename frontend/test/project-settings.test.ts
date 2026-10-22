import assert from "node:assert/strict";
import test from "node:test";
import { canManageProjectMembers, projectSettingsPayload } from "../src/lib/project-settings.ts";
import type { WorkProject } from "../src/lib/work-api.ts";

const project: WorkProject = {
  id: "project",
  user: { id: "owner" },
  collaborators: [{ id: "writer", permission: "read_write" }],
};
const values = {
  name: "  Shared project  ",
  rules: "Keep source references",
  profileID: "assistant",
  collaborators: { owner: "read_write", writer: "read_write" } as const,
};

test("a writer saves ordinary project settings without sending owner-only membership changes", () => {
  const body = JSON.parse(JSON.stringify(projectSettingsPayload(project, "writer", values)));
  assert.deepEqual(body, {
    name: "Shared project",
    rules: values.rules,
    default_profile_id: "assistant",
  });
  assert.equal(canManageProjectMembers(project, "writer"), false);
});

test("the owner can submit membership changes, including an empty list", () => {
  assert.equal(canManageProjectMembers(project, "owner"), true);
  assert.deepEqual(projectSettingsPayload(project, "owner", values).collaborators, [
    { user_id: "owner", permission: "read_write" },
    { user_id: "writer", permission: "read_write" },
  ]);
  assert.deepEqual(projectSettingsPayload(project, "owner", { ...values, collaborators: {} }).collaborators, []);
});

test("missing identity does not grant membership management and the default assistant can be cleared", () => {
  assert.equal(canManageProjectMembers({}, undefined), false);
  const body = projectSettingsPayload(project, undefined, { ...values, profileID: "" });
  assert.equal("collaborators" in body, false);
  assert.equal(body.default_profile_id, "00000000-0000-0000-0000-000000000000");
});
