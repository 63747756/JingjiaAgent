package proxy

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/workspaces"
	"github.com/chaitin/agent-compose/pkg/workspacesnapshot"
	containerapi "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/labstack/echo/v4"
)

type jingjiaAgentWorkspaceStore interface {
	GetSandbox(context.Context, string) (*domain.Sandbox, error)
	GetVMState(string) (domain.VMState, error)
}
type jingjiaAgentWorkspaceStopped func(context.Context, *domain.Sandbox, domain.VMState) bool
type jingjiaAgentWorkspaceQuiesce func(context.Context, *domain.Sandbox, domain.VMState) (func() error, error)

func RegisterJingjiaAgentRecycle(app *echo.Echo, store jingjiaAgentWorkspaceStore, removal *sandboxes.RemovalCoordinator, token string) {
	registerJingjiaAgentRecycle(app, store, removal, token, jingjiaAgentDockerStopped)
}

func registerJingjiaAgentRecycle(app *echo.Echo, store jingjiaAgentWorkspaceStore, removal *sandboxes.RemovalCoordinator, token string, stopped jingjiaAgentWorkspaceStopped) {
	app.POST("/jingjiaagent/workspace/recycle", func(c echo.Context) error {
		parts := strings.Fields(c.Request().Header.Get("Authorization"))
		if token == "" || len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || subtle.ConstantTimeCompare([]byte(parts[1]), []byte(token)) != 1 || c.Request().Header.Get("Origin") != "" {
			return c.NoContent(http.StatusUnauthorized)
		}
		id, checkpoint, expected := c.QueryParam("sandbox_id"), c.QueryParam("checkpoint_id"), c.QueryParam("expected_sha256")
		digest, hashErr := hex.DecodeString(expected)
		if !workspaces.ValidCheckpointID(id) || !workspaces.ValidCheckpointID(checkpoint) || hashErr != nil || len(digest) != 32 || len(c.QueryParams()) != 3 || len(c.QueryParams()["sandbox_id"]) != 1 || len(c.QueryParams()["checkpoint_id"]) != 1 || len(c.QueryParams()["expected_sha256"]) != 1 {
			return c.NoContent(http.StatusBadRequest)
		}
		ctx, cancel := context.WithTimeout(c.Request().Context(), 5*time.Minute)
		defer cancel()
		blocked := false
		ctx = sandboxes.WithJingjiaAgentRecycleGuard(ctx, func(sandbox *domain.Sandbox) error {
			state, err := store.GetVMState(id)
			if err != nil || sandbox.Summary.ID != id || sandbox.Summary.VMStatus != domain.VMStatusStopped || !stopped(ctx, sandbox, state) {
				blocked = true
				return errors.New("workspace must be stopped before protected recycling")
			}
			// Recompute under the same lock as removal. A committed snapshot of
			// an earlier state must never authorize deleting newer local files.
			hash := sha256.New()
			if err := workspacesnapshot.Export(ctx, sandbox.Summary.WorkspacePath, checkpoint, hash); err != nil {
				blocked = true
				return err
			}
			if subtle.ConstantTimeCompare(hash.Sum(nil), digest) != 1 {
				blocked = true
				return errors.New("工作空间与已保存的完整备份不一致")
			}
			return nil
		})
		if _, err := removal.Remove(ctx, id, false); err != nil {
			if blocked {
				return c.NoContent(http.StatusConflict)
			}
			return c.NoContent(http.StatusServiceUnavailable)
		}
		return c.NoContent(http.StatusNoContent)
	})
}

func RegisterJingjiaAgentWorkspace(app *echo.Echo, store jingjiaAgentWorkspaceStore, locks *sandboxes.LifecycleLocks, token string) {
	registerJingjiaAgentWorkspace(app, store, locks, token, jingjiaAgentDockerStopped, jingjiaAgentDockerQuiesce)
}
func registerJingjiaAgentWorkspace(app *echo.Echo, store jingjiaAgentWorkspaceStore, locks *sandboxes.LifecycleLocks, token string, stopped jingjiaAgentWorkspaceStopped, quiesce ...jingjiaAgentWorkspaceQuiesce) {
	slots := make(chan struct{}, 2)
	app.GET("/jingjiaagent/workspace/export", func(c echo.Context) error {
		parts := strings.Fields(c.Request().Header.Get("Authorization"))
		if token == "" || len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || subtle.ConstantTimeCompare([]byte(parts[1]), []byte(token)) != 1 || c.Request().Header.Get("Origin") != "" {
			return c.NoContent(http.StatusUnauthorized)
		}
		id, checkpoint := c.QueryParam("sandbox_id"), c.QueryParam("checkpoint_id")
		mode := c.QueryParam("mode")
		layout := c.QueryParam("layout")
		keys := 2
		if mode == "running" {
			keys = 3
		}
		if layout != "" {
			keys++
		}
		if !workspaces.ValidCheckpointID(id) || !workspaces.ValidCheckpointID(checkpoint) || (layout != "" && layout != "managed-v1" && layout != "full-v1" && layout != "artifacts-v1") || (layout == "full-v1" && mode != "") || (mode != "" && mode != "running") || len(c.QueryParams()) != keys || len(c.QueryParams()["sandbox_id"]) != 1 || len(c.QueryParams()["checkpoint_id"]) != 1 || (layout != "" && len(c.QueryParams()["layout"]) != 1) || (mode != "" && len(c.QueryParams()["mode"]) != 1) {
			return c.NoContent(http.StatusBadRequest)
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			return c.NoContent(http.StatusServiceUnavailable)
		}
		unlock := locks.Lock(id)
		defer unlock()
		ctx, cancel := context.WithTimeout(c.Request().Context(), 5*time.Minute)
		defer cancel()
		sandbox, err := store.GetSandbox(ctx, id)
		if err != nil || sandbox == nil {
			return c.NoContent(http.StatusNotFound)
		}
		state, err := store.GetVMState(id)
		if err != nil || sandbox.Summary.ID != id {
			return c.NoContent(http.StatusConflict)
		}
		var thaw func() error
		if mode == "running" {
			if sandbox.Summary.VMStatus != domain.VMStatusRunning || len(quiesce) != 1 {
				return c.NoContent(http.StatusConflict)
			}
			thaw, err = quiesce[0](ctx, sandbox, state)
			if err != nil {
				return c.NoContent(http.StatusServiceUnavailable)
			}
			// Includes cancellation and every failure during ZIP creation.
			defer func() { _ = thaw() }()
		} else if sandbox.Summary.VMStatus != domain.VMStatusStopped || !stopped(ctx, sandbox, state) {
			return c.NoContent(http.StatusConflict)
		}
		archive, err := os.CreateTemp("", "jingjiaagent-export-*")
		if err != nil {
			return c.NoContent(http.StatusInternalServerError)
		}
		defer os.Remove(archive.Name())
		defer archive.Close()
		export := workspaces.ExportCheckpoint
		if layout == "full-v1" {
			export = workspacesnapshot.Export
		} else if layout == "artifacts-v1" {
			export = workspacesnapshot.ExportArtifacts
		}
		if err := export(ctx, sandbox.Summary.WorkspacePath, checkpoint, archive); err != nil {
			if thaw != nil {
				if thawErr := thaw(); thawErr != nil {
					return c.NoContent(http.StatusServiceUnavailable)
				}
			}
			return c.JSON(http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		}
		// Release the processes before network transfer or object-storage work.
		// Slow readers must never leave the running environment frozen.
		if thaw != nil {
			if err := thaw(); err != nil {
				return c.NoContent(http.StatusServiceUnavailable)
			}
		}
		if _, err := archive.Seek(0, 0); err != nil {
			return c.NoContent(http.StatusInternalServerError)
		}
		c.Response().Header().Set("Content-Type", "application/zip")
		if layout == "artifacts-v1" {
			c.Response().Header().Set("Content-Type", "application/vnd.jingjiaagent.artifacts")
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		c.Response().Header().Set("Content-Disposition", `attachment; filename="checkpoint.zip"`)
		name := "checkpoint.zip"
		if layout == "artifacts-v1" {
			name = "artifacts.multipart"
			c.Response().Header().Set("Content-Disposition", `attachment; filename="artifacts.multipart"`)
		}
		// Bound slow consumers as well as ZIP generation; no lock remains forever.
		_ = http.NewResponseController(c.Response().Writer).SetWriteDeadline(time.Now().Add(5 * time.Minute))
		http.ServeContent(c.Response(), c.Request(), name, time.Time{}, archive)
		return nil
	})
}

// A running snapshot freezes processes without ending their sessions. A retried
// request also releases containers left paused by an interrupted node process.
// Only containers belonging to this Sandbox are ever inspected or changed.
func jingjiaAgentDockerQuiesce(ctx context.Context, sandbox *domain.Sandbox, state domain.VMState) (func() error, error) {
	if state.Driver != "docker" || sandbox.Summary.Driver != "docker" {
		return nil, errors.New("running checkpoint requires Docker")
	}
	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	defer docker.Close()
	boxes, err := docker.ContainerList(ctx, containerapi.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", "agent-compose.sandbox_id="+sandbox.Summary.ID))})
	if err != nil {
		return nil, err
	}
	var frozen []string
	thaw := func() error {
		if len(frozen) == 0 {
			return nil
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		releaser, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			return err
		}
		defer releaser.Close()
		var failed []string
		var result error
		for _, id := range frozen {
			inspect, err := releaser.ContainerInspect(cleanup, id)
			if err == nil && inspect.State != nil && inspect.State.Paused {
				err = releaser.ContainerUnpause(cleanup, id)
			}
			if err != nil {
				failed = append(failed, id)
				result = errors.Join(result, err)
			}
		}
		frozen = failed
		return result
	}
	for _, box := range boxes {
		inspect, err := docker.ContainerInspect(ctx, box.ID)
		if err != nil || inspect.State == nil || inspect.State.Restarting || inspect.Config == nil || inspect.Config.Labels["agent-compose.driver"] != "docker" {
			_ = thaw()
			if err == nil {
				err = errors.New("invalid checkpoint container state")
			}
			return nil, err
		}
		if !inspect.State.Running {
			continue
		}
		// Record before the RPC: a lost pause response may still have paused it.
		frozen = append(frozen, box.ID)
		if !inspect.State.Paused {
			if err := docker.ContainerPause(ctx, box.ID); err != nil {
				_ = thaw()
				return nil, err
			}
		}
	}
	if len(frozen) == 0 {
		_ = thaw()
		return nil, errors.New("running checkpoint has no running container")
	}
	return thaw, nil
}
func jingjiaAgentDockerStopped(ctx context.Context, sandbox *domain.Sandbox, state domain.VMState) bool {
	if state.Driver != "docker" || sandbox.Summary.Driver != "docker" {
		return false
	}
	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return false
	}
	defer docker.Close()
	// Also handles intentional runtime release and reconciled lost boxes without
	// trusting a stale BoxID. Every labelled Docker box must actually be stopped.
	boxes, err := docker.ContainerList(ctx, containerapi.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", "agent-compose.sandbox_id="+sandbox.Summary.ID))})
	if err != nil {
		return false
	}
	for _, box := range boxes {
		inspect, err := docker.ContainerInspect(ctx, box.ID)
		if err != nil || inspect.State == nil || inspect.State.Running || inspect.State.Restarting || inspect.Config == nil || inspect.Config.Labels["agent-compose.driver"] != "docker" {
			return false
		}
	}
	return true
}
