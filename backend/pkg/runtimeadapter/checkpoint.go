package runtimeadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/google/uuid"
)

// ExportCheckpoint fences new commands before exporting a stable workspace.
// Online saves freeze briefly on the node; policy-stopped saves stay stopped.
func (c *Client) ExportCheckpoint(ctx context.Context, tid, cid uuid.UUID, expectedEnvironment string) (io.ReadCloser, error) {
	return c.exportWorkspace(ctx, tid, cid, expectedEnvironment, false)
}

func (c *Client) ExportArtifacts(ctx context.Context, tid, id uuid.UUID, expectedEnvironment string) (io.ReadCloser, error) {
	return c.exportWorkspace(ctx, tid, id, expectedEnvironment, true)
}

func (c *Client) exportWorkspace(ctx context.Context, tid, cid uuid.UUID, expectedEnvironment string, artifacts bool) (io.ReadCloser, error) {
	e, err := c.ledger.EnvironmentForTask(ctx, tid.String())
	if err != nil {
		return nil, err
	}
	if e.ID != expectedEnvironment {
		return nil, errors.New("task environment changed before archive")
	}
	n := c.engine(e.NodeID)
	if n == nil {
		return nil, errors.New("runtime node unavailable")
	}
	tx, err := c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var current string
	if err = tx.QueryRowContext(ctx, `SELECT environment_id FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, tid).Scan(&current); err != nil {
		return nil, err
	}
	if current != e.ID {
		return nil, errors.New("task environment changed")
	}
	var state, sandbox string
	var archiving sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT state,sandbox_id,archiving_checkpoint_id::text FROM runtime_environments WHERE id=$1 FOR UPDATE`, e.ID).Scan(&state, &sandbox, &archiving); err != nil {
		return nil, err
	}
	if state == "deleted" || sandbox == "" || state == "restoring" {
		return nil, errors.New("工作环境尚未准备或已回收")
	}
	if state == "checkpointing" && (!archiving.Valid || archiving.String != cid.String()) {
		return nil, taskflow.ErrCheckpointBusy
	}
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_commands WHERE environment_id=$1 AND state IN ('pending','submitting','unknown','running'))`, e.ID).Scan(&active); err != nil {
		return nil, err
	}
	if active {
		return nil, taskflow.ErrCheckpointBusy
	}
	// Persist the lifecycle to restore after this temporary export stop. A
	// regular completed round stays online; an already sleeping environment
	// and a final archive requested by recycling must not be woken by saving.
	var resumeState, scope string
	if err = tx.QueryRowContext(ctx, `UPDATE work_storage_jobs SET request=CASE WHEN request ? 'resume_state' THEN request ELSE request || jsonb_build_object('resume_state',CASE WHEN $2='hibernated' OR ($2='checkpointing' AND command_id IS NULL) THEN 'hibernated' ELSE 'online' END) END WHERE id=$1 RETURNING request->>'resume_state',COALESCE(request->>'scope','products')`, cid, state).Scan(&resumeState, &scope); err != nil {
		return nil, err
	}
	if resumeState != "online" && resumeState != "hibernated" {
		return nil, errors.New("工作区保存的恢复状态无效")
	}
	if scope == "workspace" {
		// A message can be accepted while this final save runs, but cannot
		// execute through the environment fence. Record the executed turn
		// boundary, not wall-clock creation time of that queued message.
		if _, err = tx.ExecContext(ctx, `UPDATE work_storage_jobs SET request=request || jsonb_build_object('captured_turn',(SELECT COALESCE(max(turn),0) FROM runtime_commands WHERE task_id=$2 AND operation='task' AND submission_started)) WHERE id=$1`, cid, tid); err != nil {
			return nil, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_environments SET state='checkpointing',archiving_checkpoint_id=$2 WHERE id=$1`, e.ID, cid); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	// Older nodes reject this explicit capability request instead of silently
	// exporting repository-root folders after the managed-directory migration.
	query := url.Values{"sandbox_id": {sandbox}, "checkpoint_id": {cid.String()}, "layout": {"managed-v1"}}
	if artifacts {
		query.Set("layout", "artifacts-v1")
	}
	if scope == "workspace" {
		query.Set("layout", "full-v1")
		if resumeState != "hibernated" {
			return nil, errors.New("完整工作空间保存必须先停止写入")
		}
	}
	if resumeState == "online" {
		// The node briefly freezes Docker processes while constructing the ZIP,
		// then unfreezes before sending it. Never kill previews/background jobs
		// merely because an employee's prompt round ended.
		status, err := n.sandboxes.GetSandbox(ctx, connect.NewRequest(&v2.GetSandboxRequest{SandboxId: sandbox}))
		if err != nil {
			return nil, err
		}
		switch status.Msg.GetSandbox().GetStatus() {
		case v2.SandboxStatus_SANDBOX_STATUS_RUNNING:
			query.Set("mode", "running")
		case v2.SandboxStatus_SANDBOX_STATUS_STOPPED:
			// Complete a queued export that the old implementation stopped.
		default:
			return nil, errors.New("工作环境当前无法生成稳定保存节点")
		}
	} else {
		_, err = n.sandboxes.StopSandbox(ctx, connect.NewRequest(&v2.StopSandboxRequest{SandboxId: sandbox}))
		if err != nil {
			status, lookupErr := n.sandboxes.GetSandbox(ctx, connect.NewRequest(&v2.GetSandboxRequest{SandboxId: sandbox}))
			if lookupErr != nil || status.Msg.GetSandbox().GetStatus() != v2.SandboxStatus_SANDBOX_STATUS_STOPPED {
				return nil, err
			}
		}
	}
	address := strings.TrimRight(n.nodeURL, "/") + "/jingjiaagent/workspace/export?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{TLSClientConfig: n.tlsConfig, MaxIdleConnsPerHost: 2, ResponseHeaderTimeout: 5 * time.Minute}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: authTransport{base: transport, token: n.token}, Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		defer res.Body.Close()
		if res.StatusCode == http.StatusUnprocessableEntity {
			var detail struct {
				Error string `json:"error"`
			}
			if json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&detail) == nil && detail.Error != "" {
				return nil, fmt.Errorf("工作空间导出失败：%s", detail.Error)
			}
		}
		if scope == "workspace" && (res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusNotFound) {
			return nil, errors.New("运行节点尚不支持完整工作空间保存，请升级执行器后重试")
		}
		if artifacts && (res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusNotFound) {
			return nil, errors.New("运行节点尚不支持直接保存成果，请升级执行器后重试")
		}
		if query.Get("mode") == "running" && (res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusNotFound) {
			return nil, errors.New("运行节点尚不支持在线保存，请升级执行器镜像后重试")
		}
		return nil, errors.New("运行节点未能导出稳定工作区")
	}
	return res.Body, nil
}

func (c *Client) checkpointRemoteState(ctx context.Context, n *Engine, sandbox string) (string, error) {
	status, err := n.sandboxes.GetSandbox(ctx, connect.NewRequest(&v2.GetSandboxRequest{SandboxId: sandbox}))
	if err != nil {
		return "", err
	}
	switch status.Msg.GetSandbox().GetStatus() {
	case v2.SandboxStatus_SANDBOX_STATUS_RUNNING:
		return "online", nil
	case v2.SandboxStatus_SANDBOX_STATUS_STOPPED:
		return "hibernated", nil
	}
	return "", errors.New("运行节点尚未确认工作空间状态")
}

// CompleteCheckpoint restores the lifecycle after the immutable archive has
// committed. Retrying a lost resume reply does not re-export or duplicate a
// version. The environment fence stays closed until resume is confirmed.
func (c *Client) CompleteCheckpoint(ctx context.Context, tid, cid uuid.UUID, environmentID string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	tx, err := c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	var deleting bool
	if err = tx.QueryRowContext(ctx, `SELECT environment_id,deletion_requested FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, tid).Scan(&current, &deleting); err != nil {
		return err
	}
	if current != environmentID {
		return nil
	}
	var state, sandbox, node, owner string
	var archiving sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT state,sandbox_id,node_id,owner_id,archiving_checkpoint_id::text FROM runtime_environments WHERE id=$1 FOR UPDATE`, environmentID).Scan(&state, &sandbox, &node, &owner, &archiving); err != nil {
		return err
	}
	if state != "checkpointing" || !archiving.Valid || archiving.String != cid.String() {
		return nil
	}
	var committed bool
	var target string
	var full bool
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(CASE WHEN j.kind='artifacts' THEN j.request->>'artifacts_saved'='true' ELSE cp.state='committed' END,false),COALESCE(j.request->>'resume_state',CASE WHEN j.command_id IS NULL THEN 'hibernated' ELSE 'online' END),COALESCE(cp.manifest->>'schema'='jingjiaagent.workspace.checkpoint.v3',false) FROM work_storage_jobs j LEFT JOIN workspace_checkpoints cp ON cp.id=j.id WHERE j.id=$1 AND j.task_id=$2 AND j.environment_id=$3`, cid, tid, environmentID).Scan(&committed, &target, &full); err != nil {
		return err
	}
	if !committed || (target != "online" && target != "hibernated") {
		return errors.New("工作区保存尚未提交或恢复状态无效")
	}
	if deleting {
		target = "hibernated"
	} else if c.workGuard != nil {
		if err = c.workGuard.CheckWorkTaskAccess(ctx, uuid.MustParse(owner), tid, true); err != nil {
			if !errors.Is(err, errcode.ErrForbidden) && !errors.Is(err, errcode.ErrNotFound) {
				return err
			}
			// Preservation may finish after revocation, but must not start a
			// stopped runtime on behalf of an employee without execution access.
			target = "hibernated"
		}
	}
	if target == "online" && c.registry != nil {
		hosts, lookupErr := c.registry.ListRuntimeHosts(ctx, owner, []string{node})
		if lookupErr != nil {
			return lookupErr
		}
		if hosts[node] == nil {
			target = "hibernated"
		}
	}
	n := c.engine(node)
	if n == nil || sandbox == "" {
		return errors.New("保存已完成，等待原执行节点恢复后继续运行")
	}
	status, lookupErr := n.sandboxes.GetSandbox(ctx, connect.NewRequest(&v2.GetSandboxRequest{SandboxId: sandbox}))
	if connect.CodeOf(lookupErr) == connect.CodeNotFound {
		// Files are durable, but the runtime disappeared before finalization.
		// Let the next user turn restore the committed snapshot instead of
		// keeping the task fenced in checkpointing forever.
		if _, err = tx.ExecContext(ctx, `UPDATE runtime_environments SET state='offline',checkpoint_id=CASE WHEN $3 THEN $2::uuid ELSE NULL END,archiving_checkpoint_id=NULL WHERE id=$1`, environmentID, cid, full); err != nil {
			return err
		}
		if err = ReleaseCheckpointReservation(ctx, tx, environmentID); err != nil {
			return err
		}
		return tx.Commit()
	}
	if lookupErr != nil {
		return lookupErr
	}
	if target == "online" {
		if err = c.ledger.reserveEnvironmentTx(ctx, tx, environmentID, false); err != nil {
			return err
		}
		if status.Msg.GetSandbox().GetStatus() != v2.SandboxStatus_SANDBOX_STATUS_RUNNING {
			// Also recovers an old queued save created by the stop-based exporter.
			if _, err = n.sandboxes.ResumeSandbox(ctx, connect.NewRequest(&v2.ResumeSandboxRequest{SandboxId: sandbox})); err != nil {
				status, lookupErr = n.sandboxes.GetSandbox(ctx, connect.NewRequest(&v2.GetSandboxRequest{SandboxId: sandbox}))
				if lookupErr != nil || status.Msg.GetSandbox().GetStatus() != v2.SandboxStatus_SANDBOX_STATUS_RUNNING {
					return err
				}
			}
		}
	} else if status.Msg.GetSandbox().GetStatus() != v2.SandboxStatus_SANDBOX_STATUS_STOPPED {
		// A live export is already unfrozen. Revocation/deletion may have
		// changed the desired lifecycle while its files were being uploaded.
		if _, err = n.sandboxes.StopSandbox(ctx, connect.NewRequest(&v2.StopSandboxRequest{SandboxId: sandbox})); err != nil {
			status, lookupErr = n.sandboxes.GetSandbox(ctx, connect.NewRequest(&v2.GetSandboxRequest{SandboxId: sandbox}))
			if lookupErr != nil || status.Msg.GetSandbox().GetStatus() != v2.SandboxStatus_SANDBOX_STATUS_STOPPED {
				return err
			}
		}
	}
	// An online workspace can change again (including background processes).
	// Recycling must take a final stable snapshot instead of trusting this one.
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_environments SET state=$2,checkpoint_id=CASE WHEN $2='hibernated' AND $4 THEN $3::uuid ELSE NULL END,archiving_checkpoint_id=NULL WHERE id=$1`, environmentID, target, cid, full); err != nil {
		return err
	}
	if target == "hibernated" {
		if err = ReleaseCheckpointReservation(ctx, tx, environmentID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RestoreCheckpoint stages a new environment without changing the current task
// mapping. The mapping is switched only after preparation verifies the archive.
func (c *Client) RestoreCheckpoint(ctx context.Context, uid, tid, cid uuid.UUID, address, archiveKey, payloadSHA256 string, attachments []domain.TaskAttachment) (string, error) {
	e, err := c.ledger.EnvironmentForTask(ctx, tid.String())
	if err != nil {
		return "", err
	}
	if e.OwnerID != uid.String() {
		return "", errors.New("permission denied")
	}
	if err = c.cleanupFailedRestore(ctx, uid, tid); err != nil {
		return "", err
	}
	intent, err := c.ledger.Intent(ctx, tid.String())
	if err != nil {
		return "", err
	}
	newID := "agent_" + uuid.NewString()
	vm := e.Request
	vm.AutoHostIDs = nil // Restore stays on the original node.
	vm.ID = newID
	vm.ZipUrl = address
	var full bool
	if err = c.ledger.db.QueryRowContext(ctx, `SELECT COALESCE(manifest->>'schema'='jingjiaagent.workspace.checkpoint.v3',false) FROM workspace_checkpoints WHERE id=$1 AND task_id=$2 AND state='committed'`, cid, tid).Scan(&full); err != nil {
		return "", err
	}
	if !full && (e.Request.Git.URL != "" || (e.Request.ZipUrl != "" && !strings.Contains(e.Request.ZipUrl, "jingjiaagent-checkpoint="))) {
		return "", &RecoveryChoiceError{Message: "当前保存节点未覆盖仓库工作目录，无法安全恢复。对话和已保存成果仍可查看，请联系管理员恢复原工作区。"}
	}
	// HTTP restoration wins over Git cloning, but keep the original repository
	// identity so credential installation and later push/pull still work.
	if !full {
		vm.Git = taskflow.Git{}
	}
	vm.TaskID = tid
	intent.VMID = newID
	intent.Attachments = nil
	for _, a := range attachments {
		intent.Attachments = append(intent.Attachments, taskflow.Attachment{URL: a.URL, Filename: a.Filename})
	}
	// Resolve internal asset URLs again at resume through the work service; a
	// presigned source must never be silently passed to the raw Guest downloader.
	intent.Text = "从已保存的工作文件继续。读取 .jingjiaagent/work 和 .jingjiaagent/output 中的成果及本轮资料，按用户下一次指令处理。"
	// Use the accepted round's resources to prepare, rather than obsolete skill
	// URLs from the task's first message. Preparation never executes its prompt.
	var waitingID string
	var waitingRaw []byte
	if lookup := c.ledger.db.QueryRowContext(ctx, `SELECT id,payload FROM runtime_commands WHERE task_id=$1 AND state='waiting' AND operation='task' ORDER BY turn LIMIT 1`, tid).Scan(&waitingID, &waitingRaw); lookup == nil {
		decoded, openErr := c.ledger.open(waitingID, waitingRaw)
		if openErr != nil {
			return "", openErr
		}
		var waiting taskflow.CreateTaskReq
		if openErr = json.Unmarshal(decoded, &waiting); openErr != nil {
			return "", openErr
		}
		intent.RoundSkills, intent.AgentResources, intent.LLM, intent.Configs, intent.Env = waiting.RoundSkills, waiting.AgentResources, waiting.LLM, waiting.Configs, waiting.Env
	} else if !errors.Is(lookup, sql.ErrNoRows) {
		return "", lookup
	}
	tx, err := c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var current string
	if err = tx.QueryRowContext(ctx, `SELECT environment_id FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, tid).Scan(&current); err != nil {
		return "", err
	}
	if current != e.ID {
		return "", errors.New("task environment changed")
	}
	var existing string
	var existingCheckpoint uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT new_environment_id,checkpoint_id FROM runtime_restores WHERE task_id=$1 AND state='preparing'`, tid).Scan(&existing, &existingCheckpoint)
	if err == nil {
		if existingCheckpoint != cid {
			return "", errors.New("已有其他检查点正在恢复")
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_commands WHERE task_id=$1 AND state IN ('pending','submitting','unknown','running'))`, tid).Scan(&active); err != nil {
		return "", err
	}
	if active {
		return "", errors.New("请先停止正在执行的任务")
	}
	var generation int
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT generation,state FROM runtime_environments WHERE id=$1 FOR UPDATE`, e.ID).Scan(&generation, &state); err != nil {
		return "", err
	}
	if state != "deleted" && state != "hibernated" && state != "offline" {
		return "", errors.New("请先保存并停止原工作环境")
	}
	// Synchronize with retention even for internal callers that bypass the work
	// service's download lock. A pruning checkpoint can never be newly pinned.
	var retained bool
	if err = tx.QueryRowContext(ctx, `SELECT state='committed' FROM workspace_checkpoints WHERE id=$1 AND task_id=$2 FOR SHARE`, cid, tid).Scan(&retained); err != nil {
		return "", err
	}
	if !retained {
		return "", errors.New("保存节点已轮换清理，请使用最新成功的完整备份")
	}
	var priorTurn int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(max(turn),0) FROM runtime_commands WHERE task_id=$1 AND operation='restore_prepare'`, tid).Scan(&priorTurn); err != nil {
		return "", err
	}
	if priorTurn > generation {
		generation = priorTurn
	}
	ne := Environment{ID: newID, OwnerID: e.OwnerID, NodeID: e.NodeID, Request: vm, CreatedAt: time.Now()}
	if err = c.ledger.saveEnvironmentForRestoreTx(ctx, tx, ne, false, e.ID); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_environments SET generation=$2 WHERE id=$1`, newID, generation+1); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_restores(task_id,old_environment_id,new_environment_id,checkpoint_id,archive_key,payload_sha256) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(task_id) DO UPDATE SET old_environment_id=$2,new_environment_id=$3,checkpoint_id=$4,archive_key=$5,payload_sha256=$6,state='preparing',created_at=now()`, tid, e.ID, newID, cid, archiveKey, payloadSHA256); err != nil {
		return "", err
	}
	if err = c.ledger.enqueueTx(ctx, tx, newID, tid.String(), "restore_prepare", generation+1, mustJSON(intent)); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return newID, nil
}

func (c *Client) cleanupFailedRestore(ctx context.Context, uid, tid uuid.UUID) error {
	var failed string
	err := c.ledger.db.QueryRowContext(ctx, `SELECT new_environment_id FROM runtime_restores WHERE task_id=$1 AND state='failed'`, tid).Scan(&failed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	e, err := c.ledger.Environment(ctx, failed)
	if err != nil {
		return err
	}
	if e.State == "deleted" {
		return nil
	}
	if e.OwnerID != uid.String() {
		return errors.New("restore owner mismatch")
	}
	if e.SandboxID != "" {
		return c.VirtualMachiner().Delete(ctx, &taskflow.DeleteVirtualMachineReq{ID: failed, UserID: uid.String(), HostID: e.Request.HostID})
	}
	var uncertain bool
	if err = c.ledger.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_commands WHERE environment_id=$1 AND (submission_started OR state IN ('pending','submitting','unknown','running')))`, failed).Scan(&uncertain); err != nil {
		return err
	}
	if uncertain {
		return errors.New("失败恢复环境仍需运行节点确认")
	}
	if _, err = c.ledger.beginRecycle(ctx, failed); err != nil {
		return err
	}
	return c.ledger.completeRecycle(ctx, failed)
}

func (c *Client) commitRestore(ctx context.Context, cmd Command, e Environment) error {
	tx, err := c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var latest []byte
	var deleting bool
	if err = tx.QueryRowContext(ctx, `SELECT payload,deletion_requested FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, cmd.TaskID).Scan(&latest, &deleting); err != nil {
		return err
	}
	var deleted sql.NullTime
	if err = tx.QueryRowContext(ctx, `SELECT deleted_at FROM tasks WHERE id=$1 FOR SHARE`, cmd.TaskID).Scan(&deleted); err != nil {
		return err
	}
	if deleting || deleted.Valid {
		return errors.New("cannot restore a deleted task")
	}
	var old, state string
	if err = lockCommand(ctx, tx, cmd); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT old_environment_id,state FROM runtime_restores WHERE task_id=$1 AND new_environment_id=$2 FOR UPDATE`, cmd.TaskID, e.ID).Scan(&old, &state); err != nil {
		return err
	}
	if state == "complete" {
		return nil
	}
	var intent taskflow.CreateTaskReq
	decoded, err := c.ledger.open(cmd.TaskID, latest)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(decoded, &intent); err != nil {
		return err
	}
	intent.VMID = e.ID
	intent.Text = "从已保存的工作文件继续。读取 .jingjiaagent/work 和 .jingjiaagent/output 中的成果及本轮资料，按用户下一次指令处理。"
	intent.UserText = nil
	b := mustJSON(intent)
	sealed, err := c.ledger.seal(cmd.TaskID, b)
	if err != nil {
		return err
	}
	// Clone the product VM so the existing permission and callback path keep
	// functioning; old product/runtime records remain available for audit.
	if _, err = tx.ExecContext(ctx, `INSERT INTO virtualmachines(id,host_id,user_id,model_id,name,hostname,cores,memory,os,environment_id,access_token,expired_at) SELECT $2,host_id,user_id,model_id,name,hostname,cores,memory,os,$2,$3,now()+interval '3 hours' FROM virtualmachines WHERE id=$1 ON CONFLICT(id) DO NOTHING`, old, e.ID, uuid.NewString()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE task_virtualmachines SET virtualmachine_id=$2 WHERE task_id=$1 AND virtualmachine_id=$3`, cmd.TaskID, e.ID, old); err != nil {
		return err
	}
	// Runtime credentials retain their immutable model and follow the restored
	// task environment, including usage attribution for accepted queued rounds.
	if _, err = tx.ExecContext(ctx, `UPDATE model_api_keys SET virtualmachine_id=$2 WHERE virtualmachine_id=$1 AND user_id=$3 AND kind='runtime'`, old, e.ID, e.OwnerID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_task_intents SET environment_id=$2,payload=$3,payload_hash=$4,cancel_requested=false WHERE task_id=$1 AND environment_id=$5`, cmd.TaskID, e.ID, sealed, hash(b), old); err != nil {
		return err
	}
	// Preview identity and access policy belong to the task. Only switch its
	// route after the new workspace is verified, atomically with the task intent.
	// In particular, restore must not reopen closed or deny-all previews.
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_port_forwards SET environment_id=$2,revision=revision+1 WHERE task_id=$1 AND released_at IS NULL AND environment_id<>$2`, cmd.TaskID, e.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM runtime_task_sessions WHERE task_id=$1`, cmd.TaskID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_restores SET state='complete' WHERE task_id=$1 AND new_environment_id=$2`, cmd.TaskID, e.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE work_tasks SET delivery_status='pending',updated_at=now() WHERE task_id=$1`, cmd.TaskID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='processing',completed_at=NULL WHERE id=$1 AND deleted_at IS NULL`, cmd.TaskID); err != nil {
		return err
	}
	return tx.Commit()
}
