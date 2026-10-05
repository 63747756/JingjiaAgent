package runtimeadapter

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

type vmClient struct{ c *Client }

func (v *vmClient) Create(ctx context.Context, r *taskflow.CreateVirtualMachineReq) (*taskflow.VirtualMachine, error) {
	if r == nil {
		return nil, errors.New("missing environment request")
	}
	// The default selects only new environments. Admission retries for an
	// existing mapped ID keep their original backend, even after a rollout
	// rollback; database failures must never fall through to Taskflow.
	known := false
	if r.ID != "" {
		_, err := v.c.ledger.Environment(ctx, r.ID)
		if err == nil {
			known = true
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	if v.c.backend == "taskflow" && !known {
		old, err := v.c.legacyVM()
		if err != nil {
			return nil, err
		}
		return old.Create(ctx, r)
	}
	if _, ok := v.c.nodes[r.HostID]; !ok {
		return nil, errors.New("selected host is not a configured runtime node")
	}
	if !known && v.c.registry != nil {
		online, err := v.c.nodeReady(ctx, r.HostID)
		if err != nil {
			return nil, err
		}
		if !online {
			return nil, errors.New("selected runtime node is not ready")
		}
	}
	owner, err := uuid.Parse(r.UserID)
	if err != nil || owner == uuid.Nil {
		return nil, errors.New("invalid environment owner")
	}
	if r.ID == "" {
		r.ID = "agent_" + uuid.NewString()
	}
	e := Environment{ID: r.ID, OwnerID: r.UserID, NodeID: r.HostID, State: "pending", CreatedAt: time.Now(), Request: *r}
	if err = v.c.ledger.saveEnvironment(ctx, e, r.TaskID == uuid.Nil); err != nil {
		return nil, err
	}
	e, err = v.c.ledger.Environment(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	return projectVM(e), nil
}
func projectVM(e Environment) *taskflow.VirtualMachine {
	cores, _ := strconv.ParseInt(e.Request.Cores, 10, 32)
	return &taskflow.VirtualMachine{ID: e.ID, EnvironmentID: e.ID, HostID: e.NodeID, Hostname: e.Request.HostName, Name: e.Request.HostName, Repository: e.Request.Git.URL, Status: taskflow.VirtualMachineStatus(e.State), Cores: int32(cores), Memory: e.Request.Memory, OS: "linux", CreatedAt: e.CreatedAt.Unix()}
}
func authorize(e Environment, owner, host string) error {
	if owner != "" && owner != e.OwnerID {
		return errors.New("runtime environment permission denied")
	}
	if host != "" && host != e.NodeID {
		return errors.New("runtime node does not match environment")
	}
	return nil
}
func (v *vmClient) Delete(ctx context.Context, r *taskflow.DeleteVirtualMachineReq) error {
	if r == nil {
		return errors.New("missing environment request")
	}
	e, err := v.c.ledger.Environment(ctx, r.ID)
	if errors.Is(err, sql.ErrNoRows) {
		old, err := v.c.legacyVM()
		if err != nil {
			return err
		}
		return old.Delete(ctx, r)
	}
	if err != nil {
		return err
	}
	if e.State == "deleted" {
		return errors.New("virtual_machine not found")
	}
	if err = authorize(e, r.UserID, r.HostID); err != nil {
		return err
	}
	n := v.c.engines[e.NodeID]
	if n == nil {
		return errors.New("recorded runtime node is unavailable")
	}
	if v.c.registry != nil {
		ready, err := v.c.nodeReady(ctx, e.NodeID)
		if err != nil {
			return err
		}
		if !ready {
			return errors.New("recorded runtime node is not ready")
		}
	}
	sandbox, err := v.c.ledger.beginRecycle(ctx, e.ID)
	if err != nil {
		return err
	}
	if sandbox != "" {
		removal, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		_, err = n.sandboxes.RemoveSandbox(removal, connect.NewRequest(&v2.RemoveSandboxRequest{SandboxId: sandbox, Force: true}))
		if err != nil && connect.CodeOf(err) != connect.CodeNotFound {
			return err
		}
	}
	return v.c.ledger.completeRecycle(ctx, e.ID)
}
func (v *vmClient) Hibernate(ctx context.Context, r *taskflow.HibernateVirtualMachineReq) error {
	if r == nil {
		return errors.New("missing environment request")
	}
	e, n, err := v.c.environment(ctx, r.ID)
	if errors.Is(err, sql.ErrNoRows) {
		old, e := v.c.legacyVM()
		if e != nil {
			return e
		}
		return old.Hibernate(ctx, r)
	}
	if err != nil {
		return err
	}
	if err = authorize(e, r.UserID, r.HostID); err != nil {
		return err
	}
	return v.power(ctx, e, n, "hibernated")
}
func (v *vmClient) Resume(ctx context.Context, r *taskflow.ResumeVirtualMachineReq) error {
	if r == nil {
		return errors.New("missing environment request")
	}
	e, n, err := v.c.environment(ctx, r.ID)
	if errors.Is(err, sql.ErrNoRows) {
		old, e := v.c.legacyVM()
		if e != nil {
			return e
		}
		return old.Resume(ctx, r)
	}
	if err != nil {
		return err
	}
	if err = authorize(e, r.UserID, r.HostID); err != nil {
		return err
	}
	return v.power(ctx, e, n, "online")
}
func (v *vmClient) Info(ctx context.Context, r taskflow.VirtualMachineInfoReq) (*taskflow.VirtualMachine, error) {
	e, err := v.c.ledger.Environment(ctx, r.ID)
	if errors.Is(err, sql.ErrNoRows) {
		old, e := v.c.legacyVM()
		if e != nil {
			return nil, e
		}
		return old.Info(ctx, r)
	}
	if err != nil {
		return nil, err
	}
	if e.State == "deleted" {
		return nil, errors.New("virtual_machine not found")
	}
	if err = authorize(e, r.UserID, ""); err != nil {
		return nil, err
	}
	n, err := v.c.observationEngine(ctx, e.NodeID)
	if err != nil {
		return nil, err
	}
	vm, err := observeVM(ctx, e, n)
	if err != nil {
		return nil, err
	}
	if vm.Status == taskflow.VirtualMachineStatusOnline {
		if processes, processErr := n.processes(ctx, e.SandboxID); processErr == nil {
			vm.Processes, vm.ProcessesCollectedAt, vm.Arch, vm.Hostname = processes.Processes, processes.Collected, processes.Arch, processes.Hostname
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return vm, nil
}
func (v *vmClient) List(ctx context.Context, host string) ([]*taskflow.VirtualMachine, error) {
	out := []*taskflow.VirtualMachine{}
	if _, managed := v.c.nodes[host]; !managed && v.c.legacy != nil {
		old, err := v.c.legacy.VirtualMachiner().List(ctx, host)
		if err != nil {
			return nil, err
		}
		out = append(out, old...)
	}
	rows, err := v.c.ledger.db.QueryContext(ctx, `SELECT id FROM runtime_environments WHERE node_id=$1 AND state<>'deleted'`, host)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		vm, err := v.Info(ctx, taskflow.VirtualMachineInfoReq{ID: id})
		if err != nil {
			return nil, err
		}
		out = append(out, vm)
	}
	return out, nil
}
func (v *vmClient) IsOnline(ctx context.Context, r *taskflow.IsOnlineReq[string]) (*taskflow.IsOnlineResp, error) {
	if r == nil {
		return nil, errors.New("missing online request")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := &taskflow.IsOnlineResp{OnlineMap: map[string]bool{}, StatusMap: map[string]taskflow.VirtualMachineStatus{}}
	legacyIDs := []string{}
	group, probeContext := errgroup.WithContext(ctx)
	defer func() { cancel(); _ = group.Wait() }()
	group.SetLimit(8)
	var mu sync.Mutex
	seen := map[string]bool{}
	for _, id := range r.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		env, err := v.c.ledger.Environment(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			legacyIDs = append(legacyIDs, id)
			continue
		}
		if err != nil {
			return nil, err
		}
		// Host lists retain recycled environment records. A tombstone is an
		// offline result here, while direct file/terminal/detail access fails.
		if env.State == "deleted" {
			mu.Lock()
			out.OnlineMap[id] = false
			out.StatusMap[id] = taskflow.VirtualMachineStatusOffline
			mu.Unlock()
			continue
		}
		group.Go(func() error {
			// List probes never execute Guest commands or sample processes.
			n, err := v.c.observationEngine(probeContext, env.NodeID)
			if err != nil {
				return err
			}
			vm, err := observeVM(probeContext, env, n)
			if err != nil {
				return err
			}
			mu.Lock()
			out.OnlineMap[id] = vm.Status == taskflow.VirtualMachineStatusOnline
			out.StatusMap[id] = vm.Status
			mu.Unlock()
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(legacyIDs) > 0 {
		old, err := v.c.legacyVM()
		if err != nil {
			return nil, err
		}
		states, err := old.IsOnline(ctx, &taskflow.IsOnlineReq[string]{IDs: legacyIDs})
		if err != nil {
			return nil, err
		}
		for _, id := range legacyIDs {
			out.OnlineMap[id] = states.OnlineMap[id]
		}
	}
	return out, nil
}
func (v *vmClient) Terminal(ctx context.Context, r *taskflow.TerminalReq) (taskflow.Sheller, error) {
	if r == nil {
		return nil, errors.New("missing terminal request")
	}
	e, n, err := v.c.environment(ctx, r.ID)
	if errors.Is(err, sql.ErrNoRows) {
		old, e := v.c.legacyVM()
		if e != nil {
			return nil, e
		}
		return old.Terminal(ctx, r)
	}
	if err != nil {
		return nil, err
	}
	if err := v.readyTerminal(ctx, e, n); err != nil {
		return nil, err
	}
	return newGuestShell(ctx, n, e.SandboxID, r)
}
func (v *vmClient) TerminalList(ctx context.Context, id string) ([]*taskflow.Terminal, error) {
	e, n, err := v.c.environment(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		old, e := v.c.legacyVM()
		if e != nil {
			return nil, e
		}
		return old.TerminalList(ctx, id)
	}
	if err != nil {
		return nil, err
	}
	sandbox, err := n.sandboxes.GetSandbox(ctx, connect.NewRequest(&v2.GetSandboxRequest{SandboxId: e.SandboxID}))
	if err != nil {
		return nil, err
	}
	if sandbox.Msg.Sandbox == nil {
		return nil, errors.New("runtime returned no sandbox")
	}
	if sandbox.Msg.Sandbox.Status == v2.SandboxStatus_SANDBOX_STATUS_STOPPED {
		// A stopped Guest has no live PTYs. Listing must not wake it, and the
		// original page must still be able to offer an explicit new connection.
		return []*taskflow.Terminal{}, nil
	}
	var out []*taskflow.Terminal
	err = n.terminal(ctx, e.SandboxID, map[string]any{"op": "list"}, &out)
	return out, err
}
func (v *vmClient) CloseTerminal(ctx context.Context, r *taskflow.CloseTerminalReq) error {
	if r == nil {
		return errors.New("missing terminal request")
	}
	e, n, err := v.c.environment(ctx, r.ID)
	if errors.Is(err, sql.ErrNoRows) {
		old, e := v.c.legacyVM()
		if e != nil {
			return e
		}
		return old.CloseTerminal(ctx, r)
	}
	if err != nil {
		return err
	}
	return n.terminal(ctx, e.SandboxID, map[string]any{"op": "close", "terminal_id": r.TerminalID}, nil)
}
func (v *vmClient) Reports(ctx context.Context, r taskflow.ReportSubscribeReq) (taskflow.Reporter, error) {
	_, _, err := v.c.environment(ctx, r.ID)
	if errors.Is(err, sql.ErrNoRows) {
		old, e := v.c.legacyVM()
		if e != nil {
			return nil, e
		}
		return old.Reports(ctx, r)
	}
	if err != nil {
		return nil, err
	}
	if r.History != 0 || r.FromID != "" {
		return nil, ErrReportHistoryUnavailable
	}
	streamContext, cancel := context.WithCancel(ctx)
	return &guestReporter{client: v.c, id: r.ID, ctx: streamContext, cancel: cancel, interval: reportInterval}, nil
}

type hostClient struct{ c *Client }

func (h *hostClient) List(ctx context.Context, user string) (map[string]*taskflow.Host, error) {
	out := map[string]*taskflow.Host{}
	if h.c.legacy != nil {
		legacy, err := h.c.legacy.Host().List(ctx, user)
		if err != nil {
			return nil, err
		}
		for id, item := range legacy {
			if _, configured := h.c.nodes[id]; !configured {
				out[id] = item
			}
		}
	}
	if h.c.registry == nil {
		if h.c.legacy != nil {
			return out, nil
		}
		return nil, ErrParity
	}
	ids := make([]string, 0, len(h.c.nodes))
	for id := range h.c.nodes {
		ids = append(ids, id)
	}
	hosts, err := h.c.registry.ListRuntimeHosts(ctx, user, ids)
	if err != nil {
		return nil, err
	}
	for id, item := range hosts {
		out[id] = item
	}
	return out, nil
}
func (h *hostClient) IsOnline(ctx context.Context, r *taskflow.IsOnlineReq[string]) (*taskflow.IsOnlineResp, error) {
	if r == nil {
		return nil, errors.New("missing host online request")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := &taskflow.IsOnlineResp{OnlineMap: map[string]bool{}}
	legacyIDs := []string{}
	group, probeContext := errgroup.WithContext(ctx)
	defer func() { cancel(); _ = group.Wait() }()
	group.SetLimit(8)
	var mu sync.Mutex
	seen := map[string]bool{}
	for _, id := range r.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, configured := h.c.nodes[id]; configured && h.c.registry != nil {
			online, err := h.c.nodeReady(ctx, id)
			if err != nil {
				return nil, err
			}
			mu.Lock()
			out.OnlineMap[id] = online && h.c.engines[id] != nil
			mu.Unlock()
			continue
		}
		if n := h.c.engines[id]; n != nil {
			group.Go(func() error {
				probe, cancel := context.WithTimeout(probeContext, observationTimeout)
				defer cancel()
				_, err := n.projects.ListProjects(probe, connect.NewRequest(&v2.ListProjectsRequest{Limit: 1}))
				if callerErr := observationCallerError(probeContext, err); callerErr != nil {
					return callerErr
				}
				mu.Lock()
				out.OnlineMap[id] = err == nil
				mu.Unlock()
				return nil
			})
		} else if _, configured := h.c.nodes[id]; configured {
			mu.Lock()
			out.OnlineMap[id] = false
			mu.Unlock()
		} else {
			legacyIDs = append(legacyIDs, id)
		}
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(legacyIDs) > 0 {
		if h.c.legacy == nil {
			return nil, ErrLegacyUnavailable
		}
		old, err := h.c.legacy.Host().IsOnline(ctx, &taskflow.IsOnlineReq[string]{IDs: legacyIDs})
		if err != nil {
			return nil, err
		}
		for _, id := range legacyIDs {
			out.OnlineMap[id] = old.OnlineMap[id]
		}
	}
	return out, nil
}
