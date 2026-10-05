package runtimeadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/google/uuid"
)

// Real daemon/Docker/cgroup/Guest evidence. The preparation Run executes `true`;
// this test does not call a model or represent a full Web/Agent fault acceptance.
func TestLiveRuntimeReports(t *testing.T) {
	if os.Getenv("JINGJIAAGENT_RUNTIME_REPORT_LIVE_TEST") != "1" {
		t.Skip("requires isolated PostgreSQL and real agent-compose daemon/Guest")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	l := testLedger(t)
	node := config.RuntimeNode{ID: "report-live", URL: os.Getenv("JINGJIAAGENT_RUNTIME_TEST_URL"), TokenFile: os.Getenv("JINGJIAAGENT_RUNTIME_TEST_TOKEN_FILE"), GuestImage: os.Getenv("JINGJIAAGENT_RUNTIME_TEST_GUEST_IMAGE")}
	direct, err := NewEngine(node)
	if err != nil {
		t.Fatal("cannot configure private runtime node")
	}
	upstream, err := url.Parse(node.URL)
	if err != nil {
		t.Fatal("invalid runtime fixture URL")
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(502) }
	var startCalls, lookups atomic.Int32
	var loseResponse atomic.Bool
	loseResponse.Store(true)
	fault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agentcompose.v2.RunService/StartAgentRun":
			startCalls.Add(1)
			if loseResponse.CompareAndSwap(true, false) {
				// Forward the real submission and deliberately discard its successful
				// reply. The actual daemon still owns the admitted Run.
				recorder := httptest.NewRecorder()
				proxy.ServeHTTP(recorder, r)
				if recorder.Code != 200 {
					t.Error("real daemon did not accept lost-response fixture submission")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"code":"unavailable","message":"fixture lost admission response"}`))
				return
			}
		case "/agentcompose.v2.RunService/ListRuns":
			lookups.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	defer fault.Close()
	proxiedNode := node
	proxiedNode.URL = fault.URL
	engine, err := NewEngine(proxiedNode)
	if err != nil {
		t.Fatal("cannot configure bounded fault fixture")
	}
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer report-isolated-callback" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path != "/internal/vm-info" && r.URL.Path != "/internal/vm-ready" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer callback.Close()
	c := &Client{ledger: l, backend: "agent_compose", nodes: map[string]config.RuntimeNode{node.ID: proxiedNode}, engines: map[string]*Engine{node.ID: engine}, callbackURL: callback.URL, callbackToken: "report-isolated-callback", http: callback.Client(), logger: slog.New(slog.NewTextHandler(io.Discard, nil)), poll: 100 * time.Millisecond}
	owner := uuid.NewString()
	vm, err := c.VirtualMachiner().Create(ctx, &taskflow.CreateVirtualMachineReq{UserID: owner, HostID: node.ID, Cores: "1", Memory: 2 << 30})
	if err != nil {
		t.Fatal("independent environment admission failed")
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		env, err := l.Environment(cleanup, vm.ID)
		if err == nil && env.SandboxID == "" && env.ProjectID != "" {
			// A discarded admission reply can precede the local Sandbox mapping.
			// Locate only this test project's Run, then remove its owned sandbox.
			for i := 0; i < 30; i++ {
				runs, queryErr := direct.runs.ListRuns(cleanup, connect.NewRequest(&v2.ListRunsRequest{ProjectId: env.ProjectID, Limit: 2}))
				if queryErr != nil || len(runs.Msg.Runs) != 1 {
					break
				}
				env.SandboxID = runs.Msg.Runs[0].SandboxId
				if env.SandboxID != "" {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
		}
		if err == nil && env.SandboxID != "" && env.State != "deleted" {
			_, err = direct.sandboxes.RemoveSandbox(cleanup, connect.NewRequest(&v2.RemoveSandboxRequest{SandboxId: env.SandboxID, Force: true}))
			if err != nil && connect.CodeOf(err) != connect.CodeNotFound {
				t.Error("owned report sandbox cleanup failed")
			}
		}
	})
	if err = c.Step(ctx); err == nil {
		t.Fatal("lost submission response was acknowledged as complete")
	}
	var state, runID string
	var submitted bool
	if err = l.db.QueryRowContext(ctx, `SELECT state,run_id,submission_started FROM runtime_commands WHERE environment_id=$1 AND operation='prepare'`, vm.ID).Scan(&state, &runID, &submitted); err != nil || state != "unknown" || runID != "" || !submitted {
		t.Fatal("uncertain submission was not retained durably")
	}
	// Recreate the Worker client from the same ledger, without memory of the RPC.
	c = &Client{ledger: l, backend: c.backend, nodes: c.nodes, engines: c.engines, callbackURL: c.callbackURL, callbackToken: c.callbackToken, http: c.http, logger: c.logger, poll: c.poll}
	for ctx.Err() == nil {
		if err = c.Step(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("recreated Worker could not reconcile real preparation")
		}
		if err = l.db.QueryRowContext(ctx, `SELECT state,run_id FROM runtime_commands WHERE environment_id=$1 AND operation='prepare'`, vm.ID).Scan(&state, &runID); err != nil {
			t.Fatal("cannot inspect durable preparation")
		}
		if state == "complete" {
			break
		}
		if state == "failed" || state == "canceled" {
			t.Fatal("real preparation failed")
		}
		select {
		case <-ctx.Done():
			t.Fatal("preparation deadline")
		case <-time.After(200 * time.Millisecond):
		}
	}
	if ctx.Err() != nil {
		t.Fatal("preparation deadline")
	}
	env, err := l.Environment(ctx, vm.ID)
	if err != nil || env.SandboxID == "" || env.State != "online" || runID == "" {
		t.Fatal("preparation did not retain sandbox mapping")
	}
	var commandID string
	if err = l.db.QueryRowContext(ctx, `SELECT id FROM runtime_commands WHERE environment_id=$1 AND operation='prepare'`, vm.ID).Scan(&commandID); err != nil {
		t.Fatal("cannot inspect command correlation")
	}
	runs, err := direct.runs.ListRuns(ctx, connect.NewRequest(&v2.ListRunsRequest{ProjectId: env.ProjectID, Labels: map[string]string{"jingjiaagent_command": commandID}, Limit: 2}))
	if err != nil || len(runs.Msg.Runs) != 1 || runs.Msg.Total != 1 || startCalls.Load() != 1 || lookups.Load() < 1 {
		t.Fatal("lost reply caused duplicate admission or skipped reconciliation")
	}
	t.Log("actual preparation admission reply discarded; recreated Worker reconciled exactly one real Run")
	sample, err := c.sampleReport(ctx, vm.ID)
	if err != nil || sample.VirtualMachine.Status != taskflow.VirtualMachineStatusOnline || sample.ResourceStatus != "available" || sample.ProcessStatus != "available" {
		t.Fatal("real resource/process snapshot unavailable")
	}
	limit := sample.Metrics["memory_limit_bytes"]
	if limit.Value == nil || *limit.Value != 2<<30 || sample.Metrics["memory_usage_bytes"].Value == nil || sample.Metrics["cpu_percent"].Value == nil || len(sample.VirtualMachine.Processes) == 0 || sample.VirtualMachine.ProcessesCollectedAt <= 0 {
		t.Fatal("actual cgroup limits/processes were not reported")
	}
	cpuLimit, err := direct.execute(ctx, env.SandboxID, "cat /sys/fs/cgroup/cpu.max", 1024)
	if err != nil || strings.TrimSpace(cpuLimit) != "100000 100000" {
		t.Fatal("report fixture did not enforce its actual one-CPU limit")
	}
	for _, process := range sample.VirtualMachine.Processes {
		if process.PID <= 0 || process.Cmdline != process.ExePath || process.StartTime <= 0 {
			t.Fatal("real process identity/argument suppression contract changed")
		}
	}
	if _, err = c.VirtualMachiner().Info(ctx, taskflow.VirtualMachineInfoReq{ID: vm.ID, UserID: uuid.NewString()}); err == nil {
		t.Fatal("cross-owner Info accepted")
	}
	info, err := c.VirtualMachiner().Info(ctx, taskflow.VirtualMachineInfoReq{ID: vm.ID, UserID: owner})
	if err != nil || len(info.Processes) == 0 || info.Arch == "" {
		t.Fatal("real Info process metadata missing")
	}
	reporter, err := c.VirtualMachiner().Reports(ctx, taskflow.ReportSubscribeReq{ID: vm.ID})
	if err != nil {
		t.Fatal("report stream admission failed")
	}
	reportCount := 0
	err = reporter.BlockRead(func(entry taskflow.ReportEntry) {
		reportCount++
		var data runtimeReport
		if json.Unmarshal(entry.Data, &data) != nil || entry.Source != data.Schema || data.VirtualMachine.ID != vm.ID || data.ResourceStatus != "available" {
			t.Error("real report envelope lost snapshot")
		}
		reporter.Stop()
	})
	if !errors.Is(err, context.Canceled) || reportCount != 1 {
		t.Fatal("real report disconnect did not detach")
	}
	marker := "REPORT_FILE_" + uuid.NewString()
	chunks := make(chan []byte, 1)
	chunks <- []byte(marker)
	close(chunks)
	if err = c.FileManager().Upload(ctx, taskflow.FileReq{ID: vm.ID, UserID: owner, Path: "/workspace/report-persistence.txt"}, chunks); err != nil {
		t.Fatal("owned marker upload failed")
	}
	if err = c.VirtualMachiner().Hibernate(ctx, &taskflow.HibernateVirtualMachineReq{ID: vm.ID, UserID: owner, HostID: node.ID}); err != nil {
		t.Fatal("owned sandbox hibernate failed")
	}
	sample, err = c.sampleReport(ctx, vm.ID)
	if err != nil || sample.VirtualMachine.Status != taskflow.VirtualMachineStatusHibernated || sample.ResourceStatus != "unknown" || sample.ProcessStatus != "unknown" || len(sample.Metrics) != 0 || len(sample.VirtualMachine.Processes) != 0 {
		t.Fatal("hibernated snapshot reused stale metrics/processes")
	}
	if err = c.VirtualMachiner().Resume(ctx, &taskflow.ResumeVirtualMachineReq{ID: vm.ID, UserID: owner, HostID: node.ID}); err != nil {
		t.Fatal("owned sandbox resume failed")
	}
	sample, err = c.sampleReport(ctx, vm.ID)
	if err != nil || sample.VirtualMachine.Status != taskflow.VirtualMachineStatusOnline || sample.ProcessStatus != "available" {
		t.Fatal("resumed report did not recover")
	}
	text, err := direct.execute(ctx, env.SandboxID, "cat /workspace/report-persistence.txt", 1024)
	if err != nil || text != marker {
		t.Fatal("report stream/hibernate changed workspace content")
	}
	// Close only the owned reverse-proxy endpoint: the real daemon and existing
	// business sandboxes keep running. This exercises a real TCP refusal.
	fault.Close()
	states, err := c.Host().IsOnline(ctx, &taskflow.IsOnlineReq[string]{IDs: []string{node.ID}})
	if err != nil || states.OnlineMap[node.ID] {
		t.Fatal("closed node endpoint was not observed offline")
	}
	states, err = c.VirtualMachiner().IsOnline(ctx, &taskflow.IsOnlineReq[string]{IDs: []string{vm.ID}})
	if err != nil || states.OnlineMap[vm.ID] {
		t.Fatal("closed endpoint broke environment status query")
	}
	info, err = c.VirtualMachiner().Info(ctx, taskflow.VirtualMachineInfoReq{ID: vm.ID, UserID: owner})
	if err != nil || info.Status != taskflow.VirtualMachineStatusOffline {
		t.Fatal("closed endpoint broke environment detail")
	}
	current, err := l.Environment(ctx, vm.ID)
	if err != nil || current.State != "online" || current.SandboxID != env.SandboxID {
		t.Fatal("outage probe rewrote pinned lifecycle identity")
	}
	c.engines[node.ID] = direct
	sample, err = c.sampleReport(ctx, vm.ID)
	if err != nil || sample.ResourceStatus != "available" || sample.VirtualMachine.Status != taskflow.VirtualMachineStatusOnline {
		t.Fatal("same runtime node report could not recover")
	}
	if err = c.VirtualMachiner().Delete(ctx, &taskflow.DeleteVirtualMachineReq{ID: vm.ID, UserID: owner, HostID: node.ID}); err != nil {
		t.Fatal("owned sandbox recycle failed")
	}
	if _, err = c.sampleReport(ctx, vm.ID); err == nil {
		t.Fatal("recycled sandbox reports remained available")
	}
	t.Log("real resource limits/processes, report detach, sleep/resume files, closed-endpoint offline/recovery and recycle verified")
}
