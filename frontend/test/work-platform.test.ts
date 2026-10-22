import assert from "node:assert/strict";
import test from "node:test";
import {
  artifactURL,
  canEditArtifact,
  workPost,
  workRequest,
  workRequestID,
  workManagerTeams,
  workTaskDisplay,
  WorkApiError,
  type WorkArtifact,
  type WorkTaskInput,
} from "../src/lib/work-api.ts";

test("工作任务展示优先业务状态，保持会话可继续不等于仍未交付", () => {
  assert.deepEqual(
    workTaskDisplay({
      status: "processing",
      work: { business_status: "delivered" },
    }),
    { label: "已交付", completed: true },
  );
  assert.deepEqual(
    workTaskDisplay({
      status: "finished",
      work: { business_status: "waiting_confirmation" },
    }),
    { label: "等待确认", completed: false },
  );
  assert.deepEqual(
    workTaskDisplay({
      status: "processing",
      work: { business_status: "validating" },
    }),
    { label: "检查成果中", completed: false },
  );
  assert.deepEqual(workTaskDisplay({ status: "processing" }), {
    label: "进行中",
    completed: false,
  });
  assert.deepEqual(workTaskDisplay({ status: "finished" }), {
    label: "已结束",
    completed: true,
  });
});

test("创建工作任务固定资料版本，并通过同源会话提交真实请求", async () => {
  const original = globalThis.fetch;
  const input: WorkTaskInput = {
    content: "分析采购表并输出报告",
    resource_version_ids: ["old-pinned-version"],
    skill_ids: ["analysis-skill"],
    client_request_id: workRequestID(),
  };
  try {
    globalThis.fetch = async (path, init) => {
      assert.equal(path, "/api/v1/users/work-tasks");
      assert.equal(init?.credentials, "include");
      assert.equal(init?.method, "POST");
      assert.equal(
        new Headers(init?.headers).get("Content-Type"),
        "application/json",
      );
      const body = JSON.parse(init?.body as string);
      assert.deepEqual(body.resource_version_ids, ["old-pinned-version"]);
      assert.equal("resource_ids" in body, false);
      assert.deepEqual(body.skill_ids, ["analysis-skill"]);
      return Response.json({
        code: 0,
        data: {
          id: "task-1",
          work: { input_resources: [{ version_id: "old-pinned-version" }] },
        },
      });
    };
    const task = await workPost<{ id: string }>(
      "/api/v1/users/work-tasks",
      input,
    );
    assert.equal(task.id, "task-1");
  } finally {
    globalThis.fetch = original;
  }
});

test("权限失败和业务失败不能作为成功空状态返回", async () => {
  const original = globalThis.fetch;
  try {
    globalThis.fetch = async () =>
      Response.json({ code: 403, message: "没有项目权限" }, { status: 403 });
    await assert.rejects(
      workRequest("/api/v1/users/work/artifacts"),
      (error) =>
        error instanceof WorkApiError &&
        error.status === 403 &&
        error.message === "没有项目权限",
    );
    globalThis.fetch = async () =>
      Response.json({ code: 400, message: "资料版本已失效" });
    await assert.rejects(
      workRequest("/api/v1/users/work-tasks"),
      /资料版本已失效/,
    );
    globalThis.fetch = async () =>
      new Response("upstream unavailable", { status: 503 });
    await assert.rejects(
      workRequest("/api/v1/users/work/config"),
      (error) => error instanceof WorkApiError && error.status === 503,
    );
  } finally {
    globalThis.fetch = original;
  }
});

test("成果的下载和预览固定选择版本，特殊字符不会变成其他查询参数", () => {
  for (const action of ["preview", "download"] as const) {
    const path = artifactURL("a/b", action, "old&version=latest");
    const url = new URL(path, "http://localhost");
    assert.equal(url.pathname, `/api/v1/users/artifacts/a%2Fb/${action}`);
    assert.equal(url.searchParams.get("version_id"), "old&version=latest");
    assert.equal(url.searchParams.has("version"), false);
  }
});

test("项目共享成果缺少编辑授权时隐藏私有任务操作，明确拒绝优先于任务身份", () => {
  const artifact: WorkArtifact = {
    id: "a",
    task_id: "private-task",
    filename: "report.docx",
    current_version_id: "v1",
  };
  assert.equal(canEditArtifact(artifact), false);
  assert.equal(canEditArtifact(artifact, "another-task"), false);
  assert.equal(canEditArtifact(artifact, "private-task"), true);
  assert.equal(
    canEditArtifact({ ...artifact, can_edit: false }, "private-task"),
    false,
  );
  assert.equal(canEditArtifact({ ...artifact, can_edit: true }), true);
});

test("工作任务幂等标识使用后端可解析的UUID", () => {
  const ids = Array.from({ length: 8 }, () => workRequestID());
  assert.equal(new Set(ids).size, ids.length);
  for (const id of ids)
    assert.match(
      id,
      /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i,
    );
});

test("管理员团队取自独立管理会话，员工会话缺失时仍可管理且不借用缓存团队", () => {
  const teams = workManagerTeams({
    user: { id: "manager-user" },
    team: { id: "manager-team", name: "当前管理团队" },
  });
  assert.equal(teams.length, 1);
  assert.equal(teams[0].team_id, "manager-team");
  assert.equal(teams[0].user_id, "manager-user");
  assert.equal(teams[0].team_role, "admin");
  assert.deepEqual(workManagerTeams(null), []);
  assert.deepEqual(workManagerTeams({ team: { id: "stale-team" } }), []);
  assert.deepEqual(workManagerTeams({ user: { id: "user" } }), []);
});
