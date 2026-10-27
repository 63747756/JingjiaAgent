package runtimeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func (c *Client) previewTaskEnvironment(ctx context.Context, userID, taskID uuid.UUID) (Environment, error) {
	if userID == uuid.Nil || taskID == uuid.Nil {
		return Environment{}, errors.New("task identity is required")
	}
	env, err := c.ledger.EnvironmentForTask(ctx, taskID.String())
	if err != nil {
		return env, err
	}
	if env.OwnerID != userID.String() {
		return Environment{}, errors.New("task environment access denied")
	}
	// Lifecycle transitions are valid observations, not task access failures.
	return env, c.checkWorkEnvironmentAccess(ctx, env, true)
}

// RequestPreview is the authenticated MCP operation. It never starts a service
// and never replaces an existing mapping's employee-managed whitelist.
func (c *Client) RequestPreview(ctx context.Context, userID, taskID uuid.UUID, port int) (json.RawMessage, error) {
	if port < 1 || port > 65535 {
		return nil, errors.New("invalid preview port")
	}
	env, err := c.previewTaskEnvironment(ctx, userID, taskID)
	if err != nil {
		return nil, err
	}
	out, err := c.RequestEnvironmentPreview(ctx, env.ID, userID.String(), int32(port))
	if err != nil {
		return nil, err
	}
	c.notifyPreviewChange(ctx, taskID, true)
	return json.Marshal(out)
}

func (c *Client) ListTaskPreviews(ctx context.Context, userID, taskID uuid.UUID, requestID string) (*taskflow.ListPortforwadResp, error) {
	env, err := c.previewTaskEnvironment(ctx, userID, taskID)
	if err != nil {
		return nil, err
	}
	return c.PortForwarder().List(ctx, taskflow.ListPortforwadReq{ID: env.ID, RequestId: requestID})
}

// OpenTaskPreview is a user action. Only an explicit reopen can undo a close.
func (c *Client) OpenTaskPreview(ctx context.Context, userID, taskID uuid.UUID, port int, reopen bool) (json.RawMessage, error) {
	if port < 1 || port > 65535 {
		return nil, errors.New("invalid preview port")
	}
	env, err := c.previewTaskEnvironment(ctx, userID, taskID)
	if err != nil {
		return nil, err
	}
	out, err := c.PortForwarder().Create(ctx, taskflow.CreatePortForward{ID: env.ID, UserID: userID.String(), LocalPort: int32(port), Reopen: reopen})
	if err != nil {
		return nil, err
	}
	c.notifyPreviewChange(ctx, taskID, true)
	return json.Marshal(out)
}

// Updating the whitelist never starts a service or reopens a closed mapping.
func (c *Client) UpdateTaskPreviewWhitelist(ctx context.Context, userID, taskID uuid.UUID, port int, mode string, ips *[]string, currentIP string) (json.RawMessage, error) {
	if port < 1 || port > 65535 {
		return nil, errors.New("invalid preview port")
	}
	env, err := c.previewTaskEnvironment(ctx, userID, taskID)
	if err != nil {
		return nil, err
	}
	mapping, err := c.EnvironmentPreview(ctx, env.ID, int32(port))
	if err != nil {
		return nil, err
	}
	if mapping.ForwardID == nil {
		return nil, errors.New("preview mapping does not exist")
	}
	allowed := append([]string{}, mapping.WhitelistIPs...)
	if ips != nil {
		allowed = append([]string{}, (*ips)...)
	}
	if currentIP != "" {
		current := net.ParseIP(currentIP)
		if current == nil || current.IsUnspecified() || current.IsMulticast() {
			return nil, errors.New("cannot determine current connection IP for preview")
		}
		allowed = append(allowed, current.String())
	}
	out, err := c.PortForwarder().Update(ctx, taskflow.UpdatePortForward{ID: env.ID, ForwardID: *mapping.ForwardID, WhitelistIPs: allowed, AccessMode: mode})
	if err != nil {
		return nil, err
	}
	c.notifyPreviewChange(ctx, taskID, false)
	return json.Marshal(out)
}

func (c *Client) CloseTaskPreview(ctx context.Context, userID, taskID uuid.UUID, forwardID string) error {
	env, err := c.previewTaskEnvironment(ctx, userID, taskID)
	if err != nil {
		return err
	}
	if err = c.PortForwarder().Close(ctx, taskflow.ClosePortForward{ID: env.ID, ForwardID: forwardID}); err != nil {
		return err
	}
	c.notifyPreviewChange(ctx, taskID, false)
	return nil
}

func (c *Client) notifyPreviewChange(ctx context.Context, taskID uuid.UUID, opened bool) {
	change := "PORT_CHANGE_TYPE_UPDATED"
	if opened {
		change = "PORT_CHANGE_TYPE_OPENED"
	}
	data, _ := json.Marshal(map[string]string{"change_type": change})
	chunk, _ := json.Marshal(taskflow.TaskChunk{Event: "task-event", Kind: "port_change", Data: data, Timestamp: time.Now().UnixNano()})
	// Associate control events with the latest durable task command. They do not
	// mutate its lease or execution state, and remain visible on the control stream.
	_, err := c.ledger.db.ExecContext(ctx, `INSERT INTO runtime_events(task_id,command_id,source_key,turn,chunk)
 SELECT task_id,id,$2,turn,$3 FROM runtime_commands WHERE task_id=$1 ORDER BY turn DESC,created_at DESC LIMIT 1`, taskID, "preview:"+uuid.NewString(), string(chunk))
	if err != nil && c.logger != nil {
		c.logger.WarnContext(ctx, "preview changed; event delivery will use polling fallback", "task_id", taskID, "error", err)
	}
}
