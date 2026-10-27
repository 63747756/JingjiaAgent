package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/GoYoko/web"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/middleware"
	"github.com/63747756/jingjiaagent/backend/pkg/runtimeadapter"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/63747756/jingjiaagent/backend/pkg/ws"
)

// Control 任务控制流 WebSocket 端点
//
//	@Summary		任务控制流 WebSocket
//	@Description	数据格式约定：当前仅支持文本帧透传。服务端将 Agent 的原始文本数据包装为如下结构返回给前端（对应 domain.TaskStream）：
//	@Description	```json
//	@Description	{ "type": "string", "data": "string", "kind": "string", "timestamp": 0 }
//	@Description	```
//	@Description	独立于 stream 的长生命周期 WebSocket 连接，用于处理 call/call-response（文件浏览、diff 查看等同步请求）。
//	@Description	task 结束后连接不断开，仍可用于文件操作。
//	@Description	支持同一 taskID 多 tab 并发连接。
//	@Description
//	@Description	## 上行消息
//	@Description
//	@Description	### Type=call, Kind=repo_file_diff — 获取文件 diff
//	@Description	请求 Data:
//	@Description	```json
//	@Description	{"request_id":"string","path":"string","unified":true,"context_lines":3}
//	@Description	```
//	@Description	响应 Data:
//	@Description	```json
//	@Description	{"request_id":"string","path":"string","diff":"string","success":true,"error":"string?"}
//	@Description	```
//	@Description
//	@Description	### Type=call, Kind=repo_file_list — 列出目录文件
//	@Description	请求 Data:
//	@Description	```json
//	@Description	{"request_id":"string","path":"string","glob_pattern":"string?","include_hidden":false}
//	@Description	```
//	@Description	响应 Data:
//	@Description	```json
//	@Description	{"request_id":"string","path":"string","files":[{"name":"string","path":"string","entry_mode":0,"size":0,"modified_at":0}],"success":true,"error":"string?"}
//	@Description	```
//	@Description
//	@Description	### Type=call, Kind=repo_read_file — 读取文件内容
//	@Description	请求 Data:
//	@Description	```json
//	@Description	{"request_id":"string","path":"string","offset":0,"length":0}
//	@Description	```
//	@Description	响应 Data:
//	@Description	```json
//	@Description	{"request_id":"string","path":"string","content":"bytes","total_size":0,"offset":0,"length":0,"is_truncated":false,"success":true,"error":"string?"}
//	@Description	```
//	@Description
//	@Description	### Type=call, Kind=repo_file_changes — 查询变更文件列表
//	@Description	请求 Data:
//	@Description	```json
//	@Description	{"request_id":"string"}
//	@Description	```
//	@Description	响应 Data:
//	@Description	```json
//	@Description	{"request_id":"string","changes":[{"path":"string","status":"string","additions":0,"deletions":0,"old_path":"string?"}],"branch":"string?","commit_hash":"string?","success":true,"error":"string?"}
//	@Description	```
//	@Description
//	@Description	### Type=call, Kind=port_forward_list — 获取端口转发列表
//	@Description	请求 Data:
//	@Description	```json
//	@Description	{"request_id":"string"}
//	@Description	```
//	@Description	响应 Data:
//	@Description	```json
//	@Description	{"request_id":"string","ports":[{"port":0,"status":"string","process":"string","forward_id":"string?","access_url":"string?","label":"string?","error_message":"string?","whitelist_ips":["string"]}]}
//	@Description	```
//	@Description
//	@Description	### Type=call, Kind=restart — 重启任务
//	@Description	请求 Data:
//	@Description	```json
//	@Description	{"request_id":"string","load_session":true}
//	@Description	```
//	@Description	响应 Data:
//	@Description	```json
//	@Description	{"id":"uuid","request_id":"string?","success":true,"message":"string","session_id":"string"}
//	@Description	```
//	@Description
//	@Description	### Type=call, Kind=switch_model — 切换运行中任务模型
//	@Description	请求 Data:
//	@Description	```json
//	@Description	{"request_id":"string","model_id":"uuid","load_session":true}
//	@Description	```
//	@Description	响应 Data:
//	@Description	```json
//	@Description	{"id":"uuid","request_id":"string?","success":true,"message":"string","session_id":"string","model":{}}
//	@Description	```
//	@Description
//	@Description	### Type=call, Kind=switch_agent_resources — 运行中更新任务 skill/plugin 列表
//	@Description	请求 Data (全量声明当前所选，非增量)：
//	@Description	```json
//	@Description	{"request_id":"string","skill_ids":["uuid"],"plugin_ids":["uuid"]}
//	@Description	```
//	@Description	响应 Data:
//	@Description	```json
//	@Description	{"request_id":"string","success":true,"message":"string","session_id":"string"}
//	@Description	```
//	@Description
//	@Description	## 下行消息
//	@Description
//	@Description	- Type=call-response: 同步请求响应（Kind 与请求一致）。失败时 Data 为:
//	@Description	```json
//	@Description	{"request_id":"string","success":false,"error":"string"}
//	@Description	```
//	@Description	- restart/switch_model/switch_agent_resources additionally return status=pending|succeeded|failed; pending is non-terminal and must be resumed with the same request_id and selection.
//	@Description	- Type=task-event: 任务事件（从 TaskLive 订阅转发）
//	@Description	- Type=ping: 心跳（无 Data）
//	@Tags			【用户】任务管理
//	@Accept			json
//	@Produce		json
//	@Security		JingjiaAgentAIAuth
//	@Param			id	query		string		true	"任务 ID"
//	@Success		200	{object}	web.Resp{}	"成功"
//	@Failure		500	{object}	web.Resp	"服务器内部错误"
//	@Router			/api/v1/users/tasks/control [get]
func (h *TaskHandler) Control(c *web.Context, req domain.TaskControlReq) error {
	user := middleware.GetUser(c)

	task, _, err := h.usecase.Info(c.Request().Context(), user, req.ID)
	if err != nil {
		return err
	}

	wsConn, err := ws.Accept(c.Response().Writer, c.Request())
	if err != nil {
		h.logger.ErrorContext(c.Request().Context(), "failed to upgrade control websocket", "error", err)
		return err
	}
	defer wsConn.Close()
	if runtime, ok := h.taskflow.(*runtimeadapter.Client); ok {
		wsConn.SetRealIP(runtime.PreviewClientIP(c.Request()))
	}

	logger := h.logger.With("task_id", task.ID, "fn", "task.control")
	taskID := task.ID.String()
	// A control connection observes task state; opening a preview must never
	// wake a workspace or extend its administrator-managed idle lifetime.
	h.controlConns.Add(taskID, wsConn)
	defer h.controlConns.Remove(taskID, wsConn)

	g, ctx := errgroup.WithContext(c.Request().Context())

	g.Go(func() error {
		return h.controlPing(ctx, wsConn, taskID)
	})

	g.Go(func() error {
		return h.controlReadMessages(ctx, wsConn, logger, user, task)
	})

	g.Go(func() error {
		return h.controlSubscribeTaskEvents(ctx, wsConn, logger, taskID)
	})

	if err := g.Wait(); err != nil {
		logger.DebugContext(c.Request().Context(), "control websocket closed", "reason", err)
	}
	return nil
}

// controlPing 定期发送心跳保活
func (h *TaskHandler) controlPing(ctx context.Context, wsConn *ws.WebsocketManager, taskID string) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := wsConn.WriteJSON(domain.TaskStream{
				Type: consts.TaskStreamTypePing,
			}); err != nil {
				return fmt.Errorf("control ping failed: %w", err)
			}
		}
	}
}

// controlReadMessages 读取客户端消息并分发处理
func (h *TaskHandler) controlReadMessages(ctx context.Context, wsConn *ws.WebsocketManager, logger *slog.Logger, user *domain.User, task *domain.Task) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		d, err := wsConn.ReadMessage()
		if err != nil {
			return fmt.Errorf("control read failed: %w", err)
		}

		var m domain.TaskStream
		if err := json.Unmarshal(d, &m); err != nil {
			logger.WarnContext(ctx, "failed to unmarshal control message", "error", err, "data", string(d))
			continue
		}
		h.logger.With("task req", m, "task_id", task.ID).DebugContext(ctx, "recv task message")

		switch m.Type {
		case consts.TaskStreamTypeCall:
			h.handleControlCall(ctx, wsConn, logger, user, task, m)
		}
	}
}

// handleControlCall 处理 call 消息，调用 taskflow HTTP 接口并写回响应
func (h *TaskHandler) handleControlCall(ctx context.Context, wsConn *ws.WebsocketManager, logger *slog.Logger, user *domain.User, task *domain.Task, m domain.TaskStream) {
	taskID := task.ID.String()

	var result any
	var err error
	var requestID string

	switch m.Kind {
	case "repo_file_diff":
		var req taskflow.RepoFileDiffReq
		if err := json.Unmarshal(m.Data, &req); err != nil {
			logger.WarnContext(ctx, "failed to unmarshal request", "error", err)
			return
		}
		req.TaskId = taskID
		requestID = req.RequestId
		result, err = h.taskflow.TaskManager().FileDiff(ctx, req)

	case "repo_file_list":
		var req taskflow.RepoListFilesReq
		if err := json.Unmarshal(m.Data, &req); err != nil {
			logger.WarnContext(ctx, "failed to unmarshal request", "error", err)
			return
		}
		req.TaskId = taskID
		requestID = req.RequestId
		result, err = h.taskflow.TaskManager().ListFiles(ctx, req)

	case "repo_read_file":
		var req taskflow.RepoReadFileReq
		if err := json.Unmarshal(m.Data, &req); err != nil {
			logger.WarnContext(ctx, "failed to unmarshal request", "error", err)
			return
		}
		req.TaskId = taskID
		requestID = req.RequestId
		result, err = h.taskflow.TaskManager().ReadFile(ctx, req)

	case "repo_file_changes":
		var req taskflow.RepoFileChangesReq
		if err := json.Unmarshal(m.Data, &req); err != nil {
			logger.WarnContext(ctx, "failed to unmarshal request", "error", err)
			return
		}
		req.TaskId = taskID
		requestID = req.RequestId
		result, err = h.taskflow.TaskManager().FileChanges(ctx, req)

	case "port_forward_list":
		var req taskflow.ListPortforwadReq
		if err := json.Unmarshal(m.Data, &req); err != nil {
			logger.WarnContext(ctx, "failed to unmarshal request", "error", err)
			return
		}
		requestID = req.RequestId
		if runtime, ok := h.taskflow.(*runtimeadapter.Client); ok {
			result, err = runtime.ListTaskPreviews(ctx, user.ID, task.ID, requestID)
		} else if task.VirtualMachine != nil {
			req.ID = task.VirtualMachine.ID
			result, err = h.taskflow.PortForwarder().List(ctx, req)
		} else {
			err = fmt.Errorf("工作空间当前不可用")
		}

	case "preview_apply", "preview_whitelist", "preview_close":
		var req struct {
			RequestID    string    `json:"request_id"`
			Port         int       `json:"port"`
			ForwardID    string    `json:"forward_id"`
			Whitelist    *[]string `json:"whitelist_ips"`
			AddCurrentIP bool      `json:"add_current_ip"`
			AccessMode   string    `json:"access_mode"`
			Reopen       bool      `json:"reopen"`
		}
		if err := json.Unmarshal(m.Data, &req); err != nil {
			return
		}
		requestID = req.RequestID
		runtime, ok := h.taskflow.(*runtimeadapter.Client)
		if !ok {
			err = fmt.Errorf("当前执行环境不支持任务预览管理")
			break
		}
		if task.UserID != user.ID {
			err = fmt.Errorf("只有任务所属员工可以管理预览")
			break
		}
		if m.Kind == "preview_close" {
			err = runtime.CloseTaskPreview(ctx, user.ID, task.ID, req.ForwardID)
		} else if m.Kind == "preview_whitelist" {
			currentIP := ""
			if req.AddCurrentIP {
				currentIP = wsConn.VerifiedIP()
				if currentIP == "" {
					err = fmt.Errorf("无法确定当前连接的 IP")
					break
				}
			}
			result, err = runtime.UpdateTaskPreviewWhitelist(ctx, user.ID, task.ID, req.Port, req.AccessMode, req.Whitelist, currentIP)
		} else {
			result, err = runtime.OpenTaskPreview(ctx, user.ID, task.ID, req.Port, req.Reopen)
		}
		if err == nil {
			result = map[string]any{"request_id": requestID, "success": true, "preview": result}
		}

	case "restart":
		// Decode only the public restart fields. Execution configuration and
		// business mutations must be created by authorized server-side usecases.
		var req struct {
			RequestID   string `json:"request_id"`
			LoadSession bool   `json:"load_session"`
		}
		if err := json.Unmarshal(m.Data, &req); err != nil {
			logger.WarnContext(ctx, "failed to unmarshal restart task", "error", err)
			return
		}
		requestID = req.RequestID
		restart := taskflow.RestartTaskReq{
			ID:          task.ID,
			RequestId:   req.RequestID,
			LoadSession: req.LoadSession,
			LogStore:    string(task.LogStore),
		}
		// Observe an admitted result before current environment availability.
		if resumer, ok := h.taskflow.TaskManager().(taskflow.RestartResumer); ok && requestID != "" {
			var found bool
			result, found, err = resumer.ResumeRestart(ctx, restart)
			if found || err != nil {
				break
			}
		}
		result, err = h.taskflow.TaskManager().Restart(ctx, restart)

	case "switch_model":
		var req domain.SwitchTaskModelReq
		if err := json.Unmarshal(m.Data, &req); err != nil {
			logger.WarnContext(ctx, "failed to unmarshal switch model", "error", err)
			return
		}
		requestID = req.RequestID
		result, err = h.usecase.SwitchModel(ctx, user, task.ID, req)

	case "switch_agent_resources":
		var req domain.SwitchAgentResourcesReq
		if err := json.Unmarshal(m.Data, &req); err != nil {
			logger.WarnContext(ctx, "failed to unmarshal switch agent resources", "error", err)
			return
		}
		requestID = req.RequestID
		result, err = h.usecase.SwitchAgentResources(ctx, user, task.ID, req)

	default:
		return
	}

	if err != nil {
		logger.WarnContext(ctx, "control call failed", "error", err, "kind", m.Kind)
	}
	b, marshalErr := marshalControlResponse(m.Kind, requestID, result, err)
	if marshalErr != nil {
		logger.WarnContext(ctx, "failed to marshal control response", "error", marshalErr, "kind", m.Kind)
		return
	}

	if err := wsConn.WriteJSON(domain.TaskStream{
		Type:      consts.TaskStreamTypeCallResponse,
		Data:      b,
		Timestamp: time.Now().UnixMilli(),
		Kind:      m.Kind,
	}); err != nil {
		logger.WarnContext(ctx, "failed to write control response", "error", err, "kind", m.Kind)
	}
}

// marshalControlResponse retains the legacy success/error fields while making
// durable admission distinct from an operation's terminal outcome. Clients must
// resume pending operations with the original request ID and public selection.
func marshalControlResponse(kind, requestID string, result any, callErr error) ([]byte, error) {
	longOperation := kind == "restart" || kind == "switch_model" || kind == "switch_agent_resources"
	if callErr != nil {
		response := map[string]any{"request_id": requestID, "success": false, "error": callErr.Error()}
		if longOperation {
			response["status"] = "failed"
			if taskflow.IsRestartPending(callErr) {
				response["status"] = "pending"
			}
		}
		return json.Marshal(response)
	}
	b, err := json.Marshal(result)
	if err != nil || !longOperation {
		return b, err
	}
	var response map[string]any
	if err = json.Unmarshal(b, &response); err != nil {
		return nil, err
	}
	if response == nil {
		return json.Marshal(map[string]any{"request_id": requestID, "success": false, "status": "pending", "error": "control response is nil"})
	}
	if requestID != "" || response["request_id"] == nil {
		response["request_id"] = requestID
	}
	response["status"] = "failed"
	if response["success"] == true {
		response["status"] = "succeeded"
	}
	return json.Marshal(response)
}

// controlSubscribeTaskEvents 订阅 TaskLive，转发 task-event 事件到 control 连接。
// TaskLive 断开后自动重连，直到 ctx 取消。
func (h *TaskHandler) controlSubscribeTaskEvents(ctx context.Context, wsConn *ws.WebsocketManager, logger *slog.Logger, taskID string) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := h.taskflow.TaskLive(ctx, taskID, false, func(chunk *taskflow.TaskChunk) error {
			if chunk.Event != "task-event" {
				return nil
			}
			return wsConn.WriteJSON(domain.TaskStream{
				Type:      consts.TaskStreamTypeTaskEvent,
				Data:      chunk.Data,
				Kind:      chunk.Kind,
				Seq:       chunk.Seq,
				Timestamp: chunk.Timestamp / 1e6,
			})
		})

		if ctx.Err() != nil {
			return ctx.Err()
		}

		logger.WarnContext(ctx, "control task-event subscription disconnected, reconnecting", "error", err)
		time.Sleep(2 * time.Second)
	}
}
