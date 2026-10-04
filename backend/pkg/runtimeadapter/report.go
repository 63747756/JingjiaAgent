package runtimeadapter

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/google/uuid"
)

const observationTimeout = 3 * time.Second
const reportInterval = 5 * time.Second

// Historical Taskflow report payloads/cursor semantics are not available in
// this fork. Reject replay requests explicitly, instead of silently losing history.
var ErrReportHistoryUnavailable = errors.New("runtime report history is not supported; request a fresh snapshot")

//go:embed guest/processes.py
var processScript []byte

// Connect transmits deadlines in whole milliseconds. A server may exhaust a
// short caller budget slightly before the local context timer fires. Preserve
// that deadline instead of turning it into a successful offline observation.
func observationCallerError(ctx context.Context, rpcErr error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	code := connect.CodeOf(rpcErr)
	if code == connect.CodeDeadlineExceeded || code == connect.CodeCanceled {
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < observationTimeout {
			return context.DeadlineExceeded
		}
	}
	return nil
}

type processSnapshot struct {
	Processes []taskflow.Process `json:"processes"`
	Collected int64              `json:"processes_collected_at"`
	Arch      string             `json:"arch"`
	Hostname  string             `json:"hostname"`
}

func (e *Engine) processes(ctx context.Context, sandbox string) (processSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, observationTimeout)
	defer cancel()
	command := "python3 -c \"import base64;exec(compile(base64.b64decode('" + base64.StdEncoding.EncodeToString(processScript) + "'),'<snapshot>','exec'))\""
	text, err := e.execute(ctx, sandbox, command, 2<<20)
	var out processSnapshot
	if err != nil {
		return out, err
	}
	if json.Unmarshal([]byte(text), &out) != nil || out.Collected <= 0 || len(out.Processes) > 2048 {
		return processSnapshot{}, errors.New("invalid Guest process snapshot")
	}
	return out, nil
}

// Observation never writes lifecycle state or admits a Run. A transport outage
// means offline for list/detail queries, not a durable environment failure.
func observeVM(ctx context.Context, e Environment, n *Engine) (*taskflow.VirtualMachine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	vm := projectVM(e)
	if e.SandboxID == "" {
		return vm, nil
	}
	if n == nil {
		vm.Status, vm.StatusMessage = taskflow.VirtualMachineStatusOffline, "Recorded runtime node is unavailable"
		return vm, nil
	}
	probe, cancel := context.WithTimeout(ctx, observationTimeout)
	defer cancel()
	r, err := n.sandboxes.GetSandbox(probe, connect.NewRequest(&v2.GetSandboxRequest{SandboxId: e.SandboxID}))
	if callerErr := observationCallerError(ctx, err); callerErr != nil {
		return nil, callerErr
	}
	if err != nil {
		switch connect.CodeOf(err) {
		case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeCanceled, connect.CodeNotFound:
			vm.Status, vm.StatusMessage = taskflow.VirtualMachineStatusOffline, "Remote sandbox is unavailable"
			return vm, nil
		default:
			// Permission/configuration errors must not masquerade as a healthy query.
			return nil, errors.New("runtime sandbox status query failed")
		}
	}
	sandbox := r.Msg.GetSandbox()
	if sandbox == nil || sandbox.SandboxId != e.SandboxID {
		return nil, errors.New("runtime returned an invalid sandbox observation")
	}
	switch sandbox.Status {
	case v2.SandboxStatus_SANDBOX_STATUS_RUNNING:
		vm.Status = taskflow.VirtualMachineStatusOnline
	case v2.SandboxStatus_SANDBOX_STATUS_STOPPED:
		vm.Status = taskflow.VirtualMachineStatusHibernated
	case v2.SandboxStatus_SANDBOX_STATUS_PENDING:
		vm.Status = taskflow.VirtualMachineStatusPending
	default:
		vm.Status = taskflow.VirtualMachineStatusOffline
	}
	return vm, nil
}

type reportMetric struct {
	Value  *float64 `json:"value,omitempty"`
	Unit   string   `json:"unit"`
	Status string   `json:"status"`
}

func projectMetric(m *v2.MetricValue, unit string) reportMetric {
	out := reportMetric{Unit: unit, Status: "unknown"}
	if m != nil && m.Status == v2.MetricStatus_METRIC_STATUS_OK && m.Unit == unit && m.Value != nil && !math.IsNaN(*m.Value) && !math.IsInf(*m.Value, 0) && *m.Value >= 0 {
		value := *m.Value
		out.Value, out.Status = &value, "ok"
	}
	return out
}

// Private payload inside the existing ReportEntry envelope. The original fork
// has no ReportEntry consumer or business reports route; no new UI is introduced.
type runtimeReport struct {
	Schema         string                   `json:"schema"`
	VirtualMachine *taskflow.VirtualMachine `json:"virtual_machine"`
	SampledAt      int64                    `json:"sampled_at"`
	ResourceStatus string                   `json:"resource_status"`
	ProcessStatus  string                   `json:"process_status"`
	Metrics        map[string]reportMetric  `json:"metrics"`
}

func (c *Client) sampleReport(ctx context.Context, id string) (runtimeReport, error) {
	e, err := c.ledger.Environment(ctx, id)
	if err != nil {
		return runtimeReport{}, err
	}
	if e.State == "deleted" {
		return runtimeReport{}, errors.New("virtual_machine not found")
	}
	n, err := c.observationEngine(ctx, e.NodeID)
	if err != nil {
		return runtimeReport{}, err
	}
	vm, err := observeVM(ctx, e, n)
	if err != nil {
		return runtimeReport{}, err
	}
	out := runtimeReport{Schema: "monkeycode.runtime.snapshot.v1", VirtualMachine: vm, SampledAt: time.Now().Unix(), ResourceStatus: "unknown", ProcessStatus: "unknown", Metrics: map[string]reportMetric{}}
	if vm.Status != taskflow.VirtualMachineStatusOnline {
		return out, nil
	}
	probe, cancel := context.WithTimeout(ctx, observationTimeout)
	r, statsErr := n.sandboxes.GetSandboxStats(probe, connect.NewRequest(&v2.GetSandboxStatsRequest{SandboxId: e.SandboxID}))
	cancel()
	if statsErr == nil && r.Msg.GetStats() != nil && r.Msg.Stats.SandboxId == e.SandboxID {
		s := r.Msg.Stats
		out.ResourceStatus = "available"
		if s.SampledAt != nil && s.SampledAt.CheckValid() == nil {
			out.SampledAt = s.SampledAt.AsTime().Unix()
		}
		out.Metrics = map[string]reportMetric{
			"cpu_percent": projectMetric(s.CpuPercent, "percent"), "memory_usage_bytes": projectMetric(s.MemoryUsageBytes, "bytes"),
			"memory_limit_bytes": projectMetric(s.MemoryLimitBytes, "bytes"), "memory_percent": projectMetric(s.MemoryPercent, "percent"),
			"network_rx_bytes": projectMetric(s.NetworkRxBytes, "bytes"), "network_tx_bytes": projectMetric(s.NetworkTxBytes, "bytes"),
			"block_read_bytes": projectMetric(s.BlockReadBytes, "bytes"), "block_write_bytes": projectMetric(s.BlockWriteBytes, "bytes"),
			"uptime_seconds": projectMetric(s.UptimeSeconds, "seconds"),
		}
	}
	if processes, processErr := n.processes(ctx, e.SandboxID); processErr == nil {
		vm.Processes, vm.ProcessesCollectedAt, vm.Arch, vm.Hostname = processes.Processes, processes.Collected, processes.Arch, processes.Hostname
		out.ProcessStatus = "available"
	}
	if ctx.Err() != nil {
		return runtimeReport{}, ctx.Err()
	}
	return out, nil
}

type guestReporter struct {
	client   *Client
	id       string
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	reading  bool
	interval time.Duration
}

func (r *guestReporter) Stop() { r.cancel() }
func (r *guestReporter) BlockRead(fn func(taskflow.ReportEntry)) error {
	if fn == nil {
		return errors.New("report callback is required")
	}
	r.mu.Lock()
	if r.reading {
		r.mu.Unlock()
		return errors.New("report stream already has a reader")
	}
	r.reading = true
	r.mu.Unlock()
	defer r.Stop()
	for {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		sample, err := r.client.sampleReport(r.ctx, r.id)
		if err != nil {
			return err
		}
		data, err := json.Marshal(sample)
		if err != nil {
			return err
		}
		if err = r.ctx.Err(); err != nil {
			return err
		}
		fn(taskflow.ReportEntry{ID: uuid.NewString(), Source: "monkeycode.runtime.snapshot.v1", Ts: sample.SampledAt, Data: data})
		timer := time.NewTimer(r.interval)
		select {
		case <-r.ctx.Done():
			timer.Stop()
			return r.ctx.Err()
		case <-timer.C:
		}
	}
}
