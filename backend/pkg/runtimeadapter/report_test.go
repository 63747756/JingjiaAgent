package runtimeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"github.com/google/uuid"
)

type reportTestServer struct {
	rpc.UnimplementedSandboxServiceHandler
	rpc.UnimplementedProjectServiceHandler
	rpc.UnimplementedExecServiceHandler
	mu                                sync.Mutex
	status                            v2.SandboxStatus
	code                              connect.Code
	block                             bool
	statsUnsupported                  bool
	probeCalls, execCalls, statsCalls int
}

func (s *reportTestServer) GetSandbox(ctx context.Context, r *connect.Request[v2.GetSandboxRequest]) (*connect.Response[v2.GetSandboxResponse], error) {
	s.mu.Lock()
	s.probeCalls++
	status, code, block := s.status, s.code, s.block
	s.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if code != 0 {
		return nil, connect.NewError(code, errors.New("secret must not enter public snapshot"))
	}
	return connect.NewResponse(&v2.GetSandboxResponse{Sandbox: &v2.Sandbox{SandboxId: r.Msg.SandboxId, Status: status}}), nil
}
func (s *reportTestServer) ListProjects(ctx context.Context, _ *connect.Request[v2.ListProjectsRequest]) (*connect.Response[v2.ListProjectsResponse], error) {
	s.mu.Lock()
	s.probeCalls++
	code, block := s.code, s.block
	s.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if code != 0 {
		return nil, connect.NewError(code, errors.New("private diagnostic"))
	}
	return connect.NewResponse(&v2.ListProjectsResponse{}), nil
}
func (s *reportTestServer) GetSandboxStats(_ context.Context, r *connect.Request[v2.GetSandboxStatsRequest]) (*connect.Response[v2.GetSandboxStatsResponse], error) {
	s.mu.Lock()
	s.statsCalls++
	unsupported := s.statsUnsupported
	s.mu.Unlock()
	if unsupported {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("not implemented"))
	}
	value := float64(2 << 30)
	return connect.NewResponse(&v2.GetSandboxStatsResponse{Stats: &v2.SandboxStats{SandboxId: r.Msg.SandboxId, MemoryLimitBytes: &v2.MetricValue{Value: &value, Unit: "bytes", Status: v2.MetricStatus_METRIC_STATUS_OK}}}), nil
}
func (s *reportTestServer) Exec(_ context.Context, _ *connect.Request[v2.ExecRequest]) (*connect.Response[v2.ExecResponse], error) {
	s.mu.Lock()
	s.execCalls++
	s.mu.Unlock()
	return connect.NewResponse(&v2.ExecResponse{Result: &v2.ExecResult{Stdout: `{"processes":[{"pid":1,"exepath":"/usr/bin/node","cmdline":"/usr/bin/node","start_time":10}],"processes_collected_at":20,"arch":"x86_64","hostname":"test-guest"}`}}), nil
}
func reportEngine(t *testing.T, s *reportTestServer) *Engine {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := rpc.NewSandboxServiceHandler(s)
	mux.Handle(path, handler)
	path, handler = rpc.NewProjectServiceHandler(s)
	mux.Handle(path, handler)
	path, handler = rpc.NewExecServiceHandler(s)
	mux.Handle(path, handler)
	return testEngine(t, mux)
}

func TestSandboxObservationDoesNotFabricateOnlineOrLeakDiagnostics(t *testing.T) {
	s := &reportTestServer{status: v2.SandboxStatus_SANDBOX_STATUS_RUNNING}
	n := reportEngine(t, s)
	e := Environment{ID: "env", SandboxID: "sandbox", State: "online"}
	for _, tt := range []struct {
		code   connect.Code
		status v2.SandboxStatus
		want   taskflow.VirtualMachineStatus
		fail   bool
	}{
		{0, v2.SandboxStatus_SANDBOX_STATUS_RUNNING, taskflow.VirtualMachineStatusOnline, false},
		{0, v2.SandboxStatus_SANDBOX_STATUS_STOPPED, taskflow.VirtualMachineStatusHibernated, false},
		{0, v2.SandboxStatus_SANDBOX_STATUS_PENDING, taskflow.VirtualMachineStatusPending, false},
		{connect.CodeUnavailable, 0, taskflow.VirtualMachineStatusOffline, false},
		{connect.CodeDeadlineExceeded, 0, taskflow.VirtualMachineStatusOffline, false},
		{connect.CodeNotFound, 0, taskflow.VirtualMachineStatusOffline, false},
		{connect.CodeUnauthenticated, 0, "", true},
	} {
		s.mu.Lock()
		s.code, s.status = tt.code, tt.status
		s.mu.Unlock()
		vm, err := observeVM(context.Background(), e, n)
		if (err != nil) != tt.fail || (!tt.fail && vm.Status != tt.want) {
			t.Fatalf("observation: code=%v status=%v error=%v", tt.code, vm, err)
		}
		if err != nil && err.Error() != "runtime sandbox status query failed" {
			t.Fatal("raw RPC diagnostic leaked")
		}
	}
	vm, err := observeVM(context.Background(), e, nil)
	if err != nil || vm.Status != taskflow.VirtualMachineStatusOffline {
		t.Fatal("removed node did not observe offline")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := observeVM(ctx, e, n); !errors.Is(err, context.Canceled) {
		t.Fatal("caller cancellation swallowed")
	}
}

func TestMetricUnknownIsNotZeroAndRejectsInvalidSamples(t *testing.T) {
	for _, tt := range []struct {
		value  float64
		unit   string
		status v2.MetricStatus
		valid  bool
	}{
		{0, "bytes", v2.MetricStatus_METRIC_STATUS_OK, true},
		{3, "bytes", v2.MetricStatus_METRIC_STATUS_UNKNOWN, false},
		{3, "percent", v2.MetricStatus_METRIC_STATUS_OK, false},
		{-1, "bytes", v2.MetricStatus_METRIC_STATUS_OK, false},
		{math.NaN(), "bytes", v2.MetricStatus_METRIC_STATUS_OK, false},
		{math.Inf(1), "bytes", v2.MetricStatus_METRIC_STATUS_OK, false},
	} {
		value := tt.value
		m := projectMetric(&v2.MetricValue{Value: &value, Unit: tt.unit, Status: tt.status}, "bytes")
		if (m.Value != nil) != tt.valid {
			t.Fatal("invalid/unknown metric became a measured zero")
		}
	}
	if projectMetric(nil, "bytes").Value != nil {
		t.Fatal("missing metric became zero")
	}
}

func TestReportsSnapshotCancellationRecycleAndNoListExec(t *testing.T) {
	l := testLedger(t)
	s := &reportTestServer{status: v2.SandboxStatus_SANDBOX_STATUS_RUNNING}
	n := reportEngine(t, s)
	c := &Client{ledger: l, engines: map[string]*Engine{"node": n}, nodes: map[string]config.RuntimeNode{"node": {ID: "node"}}}
	e := Environment{ID: "env", OwnerID: uuid.NewString(), NodeID: "node", Request: taskflow.CreateVirtualMachineReq{Cores: "1", Memory: 2 << 30}}
	ctx := context.Background()
	if err := l.SaveEnvironment(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := l.SetEnvironment(ctx, e.ID, "project", "sandbox", "online"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		states, err := c.VirtualMachiner().IsOnline(ctx, &taskflow.IsOnlineReq[string]{IDs: []string{e.ID, e.ID}})
		if err != nil || !states.OnlineMap[e.ID] {
			t.Fatal("online list failed")
		}
	}
	s.mu.Lock()
	calls, execs := s.probeCalls, s.execCalls
	s.mu.Unlock()
	if calls != 2 || execs != 0 {
		t.Fatal("list repeated IDs or executed a Guest command")
	}
	if _, err := c.VirtualMachiner().Info(ctx, taskflow.VirtualMachineInfoReq{ID: e.ID, UserID: uuid.NewString()}); err == nil {
		t.Fatal("cross-user snapshot accepted")
	}
	info, err := c.VirtualMachiner().Info(ctx, taskflow.VirtualMachineInfoReq{ID: e.ID, UserID: e.OwnerID})
	if err != nil || len(info.Processes) != 1 || info.ProcessesCollectedAt != 20 || info.Arch != "x86_64" {
		t.Fatal("process metadata not projected")
	}
	for _, request := range []taskflow.ReportSubscribeReq{{ID: e.ID, History: 1}, {ID: e.ID, FromID: "cursor"}} {
		if _, err := c.VirtualMachiner().Reports(ctx, request); !errors.Is(err, ErrReportHistoryUnavailable) {
			t.Fatal("unsupported report replay silently accepted")
		}
	}
	r, err := c.VirtualMachiner().Reports(ctx, taskflow.ReportSubscribeReq{ID: e.ID})
	if err != nil {
		t.Fatal(err)
	}
	r.(*guestReporter).interval = time.Millisecond
	count := 0
	err = r.BlockRead(func(entry taskflow.ReportEntry) {
		count++
		var data runtimeReport
		if json.Unmarshal(entry.Data, &data) != nil || entry.ID == "" || entry.Source != data.Schema || data.ProcessStatus != "available" || data.Metrics["memory_limit_bytes"].Value == nil {
			t.Error("report envelope or snapshot lost data")
		}
		r.Stop()
		r.Stop()
	})
	if count != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("report Stop failed: count=%d err=%v", count, err)
	}
	s.mu.Lock()
	s.code = connect.CodeUnavailable
	s.mu.Unlock()
	sample, err := c.sampleReport(ctx, e.ID)
	if err != nil || sample.VirtualMachine.Status != taskflow.VirtualMachineStatusOffline || sample.ResourceStatus != "unknown" || sample.ProcessStatus != "unknown" || len(sample.Metrics) != 0 {
		t.Fatal("offline metrics fabricated or stale processes reused")
	}
	current, err := l.Environment(ctx, e.ID)
	if err != nil || current.State != "online" {
		t.Fatal("read-only outage probe changed durable lifecycle state")
	}
	s.mu.Lock()
	s.code = 0
	s.statsUnsupported = true
	s.mu.Unlock()
	sample, err = c.sampleReport(ctx, e.ID)
	if err != nil || sample.ResourceStatus != "unknown" || sample.ProcessStatus != "available" {
		t.Fatal("unsupported driver stats lost usable process sample")
	}
	s.mu.Lock()
	s.block = true
	s.mu.Unlock()
	r, err = c.VirtualMachiner().Reports(ctx, taskflow.ReportSubscribeReq{ID: e.ID})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- r.BlockRead(func(taskflow.ReportEntry) { t.Error("canceled query emitted a sample") }) }()
	time.Sleep(30 * time.Millisecond)
	r.Stop()
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel in-flight RPC")
	}
	if err := l.SetEnvironment(ctx, e.ID, "project", "sandbox", "deleted"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.sampleReport(ctx, e.ID); err == nil {
		t.Fatal("recycled snapshot still readable")
	}
}

func TestHostProbesBoundedDeduplicatedAndCancelAware(t *testing.T) {
	s := &reportTestServer{}
	n := reportEngine(t, s)
	c := &Client{engines: map[string]*Engine{"node": n}, nodes: map[string]config.RuntimeNode{"node": {ID: "node"}, "missing": {ID: "missing"}}}
	states, err := c.Host().IsOnline(context.Background(), &taskflow.IsOnlineReq[string]{IDs: []string{"node", "node", "missing"}})
	if err != nil || !states.OnlineMap["node"] || states.OnlineMap["missing"] {
		t.Fatal("configured host routing failed")
	}
	s.mu.Lock()
	count := s.probeCalls
	s.block = true
	s.mu.Unlock()
	if count != 1 {
		t.Fatal("duplicate host IDs caused repeated probes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.Host().IsOnline(ctx, &taskflow.IsOnlineReq[string]{IDs: []string{"node"}}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("host query swallowed caller deadline")
	}
	start := time.Now()
	states, err = c.Host().IsOnline(context.Background(), &taskflow.IsOnlineReq[string]{IDs: []string{"node"}})
	if err != nil || states.OnlineMap["node"] || time.Since(start) > 4*time.Second {
		t.Fatal("node heartbeat probe was not bounded")
	}
}

type reportLegacyVM struct {
	taskflow.VirtualMachiner
	request taskflow.ReportSubscribeReq
}

func (s *reportLegacyVM) Reports(_ context.Context, request taskflow.ReportSubscribeReq) (taskflow.Reporter, error) {
	s.request = request
	return nil, nil
}

type reportLegacyHost struct {
	calls int
	ids   []string
}

func (s *reportLegacyHost) List(context.Context, string) (map[string]*taskflow.Host, error) {
	return nil, nil
}
func (s *reportLegacyHost) IsOnline(_ context.Context, request *taskflow.IsOnlineReq[string]) (*taskflow.IsOnlineResp, error) {
	s.calls++
	s.ids = append([]string(nil), request.IDs...)
	states := map[string]bool{}
	for _, id := range request.IDs {
		states[id] = true
	}
	return &taskflow.IsOnlineResp{OnlineMap: states}, nil
}

type reportLegacyClient struct {
	taskflow.Clienter
	vm   *reportLegacyVM
	host *reportLegacyHost
}

func (s *reportLegacyClient) VirtualMachiner() taskflow.VirtualMachiner { return s.vm }
func (s *reportLegacyClient) Host() taskflow.Hoster                     { return s.host }

func TestReportsAndMixedHostQueriesPreserveLegacyRouting(t *testing.T) {
	l := testLedger(t)
	old := &reportLegacyClient{vm: &reportLegacyVM{}, host: &reportLegacyHost{}}
	c := &Client{ledger: l, legacy: old, nodes: map[string]config.RuntimeNode{"removed": {ID: "removed"}}, engines: map[string]*Engine{}}
	request := taskflow.ReportSubscribeReq{ID: "legacy-env", History: 12, FromID: "original-cursor"}
	if _, err := c.VirtualMachiner().Reports(context.Background(), request); err != nil || old.vm.request != request {
		t.Fatal("legacy report replay contract changed")
	}
	states, err := c.Host().IsOnline(context.Background(), &taskflow.IsOnlineReq[string]{IDs: []string{"removed", "legacy-one", "legacy-one", "legacy-two"}})
	if err != nil || states.OnlineMap["removed"] || !states.OnlineMap["legacy-one"] || !states.OnlineMap["legacy-two"] || old.host.calls != 1 || len(old.host.ids) != 2 {
		t.Fatal("mixed nodes were rerouted or legacy queries were not batched")
	}
}
