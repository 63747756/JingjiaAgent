package runtimeadapter

import (
	"context"
	"database/sql"
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
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/google/uuid"
)

// Two actual daemons, one actual Docker engine, isolated PostgreSQL schema.
// Preparation runs `true`; this is resource/lifecycle evidence, not model QA.
func TestLiveRuntimeCapacity(t *testing.T) {
	if os.Getenv("JINGJIAAGENT_RUNTIME_CAPACITY_LIVE_TEST") != "1" {
		t.Skip("requires isolated PostgreSQL and two real patched daemons")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	l := testLedger(t)
	l.capacity = config.RuntimeCapacity{Enabled: true, ReserveCPUMillis: 1000, ReserveMemoryBytes: 1 << 30, MaxCPUMillis: 1000, MaxMemoryBytes: 2 << 30}
	nodes := map[string]config.RuntimeNode{}
	engines := map[string]*Engine{}
	for index, id := range []string{"capacity-a", "capacity-b"} {
		endpoint := os.Getenv("JINGJIAAGENT_RUNTIME_TEST_URL")
		if index == 1 {
			endpoint = os.Getenv("JINGJIAAGENT_RUNTIME_TEST_SECOND_URL")
		}
		node := config.RuntimeNode{ID: id, URL: endpoint, TokenFile: os.Getenv("JINGJIAAGENT_RUNTIME_TEST_TOKEN_FILE"), GuestImage: os.Getenv("JINGJIAAGENT_RUNTIME_TEST_GUEST_IMAGE")}
		engine, err := NewEngine(node)
		if err != nil {
			t.Fatal("cannot configure capacity daemon")
		}
		nodes[id] = node
		engines[id] = engine
	}
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer callback.Close()
	c := &Client{ledger: l, backend: "agent_compose", nodes: nodes, engines: engines, registry: &nodeRegistryFixture{}, callbackURL: callback.URL, callbackToken: "capacity-test-only", http: callback.Client(), logger: slog.New(slog.NewTextHandler(io.Discard, nil)), poll: 100 * time.Millisecond}
	if err := c.SyncNodes(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := engines["capacity-a"].nodeSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := engines["capacity-b"].nodeSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a.InstanceID == b.InstanceID || a.Fingerprint == b.Fingerprint || a.CapacityID == "" || a.CapacityID != b.CapacityID {
		t.Fatal("actual Docker engine aliases not grouped")
	}
	t.Logf("two independent daemon identities share one Docker capacity pool; reported host %d CPU / %d bytes", a.Cores, a.Memory)
	owner := uuid.NewString()
	create := func(node string) (*taskflow.VirtualMachine, error) {
		return c.VirtualMachiner().Create(ctx, &taskflow.CreateVirtualMachineReq{UserID: owner, HostID: node, Cores: "1", Memory: 2 << 30})
	}
	vm, err := create("capacity-a")
	if err != nil {
		t.Fatal("first capacity admission failed", err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		e, err := l.Environment(cleanup, vm.ID)
		if err == nil && e.SandboxID != "" {
			_, _ = engines["capacity-a"].sandboxes.RemoveSandbox(cleanup, connect.NewRequest(&v2.RemoveSandboxRequest{SandboxId: e.SandboxID, Force: true}))
		}
	})
	rejected := func() {
		t.Helper()
		if _, err := create("capacity-b"); !errors.Is(err, errcode.ErrRuntimeCapacityExhausted) {
			t.Fatal("shared pool overcommit accepted", err)
		}
	}
	rejected()
	for {
		if err = c.SyncNodes(ctx); err != nil {
			t.Fatal(err)
		}
		if err = c.Step(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("real preparation failed", err)
		}
		var state string
		if err = l.db.QueryRowContext(ctx, `SELECT state FROM runtime_commands WHERE environment_id=$1`, vm.ID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "complete" {
			break
		}
		if state == "failed" || state == "canceled" {
			t.Fatal("preparation ended unsuccessfully")
		}
		select {
		case <-ctx.Done():
			t.Fatal("capacity live preparation timed out")
		case <-time.After(250 * time.Millisecond):
		}
	}
	env, err := l.Environment(ctx, vm.ID)
	if err != nil || env.SandboxID == "" {
		t.Fatal("real Sandbox mapping missing")
	}
	output, err := engines["capacity-a"].execute(ctx, env.SandboxID, "cat /sys/fs/cgroup/cpu.max; cat /sys/fs/cgroup/memory.max", 1024)
	if err != nil || strings.TrimSpace(output) != "100000 100000\n2147483648" {
		t.Fatal("actual cgroups do not match reserved resources")
	}
	rejected() // KEEP_RUNNING completion still holds resources.
	if err = c.VirtualMachiner().Hibernate(ctx, &taskflow.HibernateVirtualMachineReq{ID: vm.ID, UserID: owner}); err != nil {
		t.Fatal(err)
	}
	rejected()
	if err = c.VirtualMachiner().Resume(ctx, &taskflow.ResumeVirtualMachineReq{ID: vm.ID, UserID: owner}); err != nil {
		t.Fatal(err)
	}
	// Fresh client has no in-memory admission state; saved reservation survives.
	c = &Client{ledger: l, backend: c.backend, nodes: c.nodes, engines: c.engines, registry: &nodeRegistryFixture{}, callbackURL: c.callbackURL, callbackToken: c.callbackToken, http: c.http, logger: c.logger, poll: c.poll}
	if err = c.SyncNodes(ctx); err != nil {
		t.Fatal(err)
	}
	rejected()
	t.Log("real preparation/KEEP_RUNNING, actual CPU/RAM cgroups, sleep/resume and recreated Worker retain one reservation")
	direct := engines["capacity-a"]
	target, _ := url.Parse(nodes["capacity-a"].URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var lose atomic.Bool
	lose.Store(true)
	fault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agentcompose.v2.SandboxService/RemoveSandbox" && lose.CompareAndSwap(true, false) {
			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, r)
			if rec.Code != 200 {
				t.Error("real removal was not successful")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"code":"unavailable","message":"fixture discarded removal reply"}`))
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer fault.Close()
	proxied := nodes["capacity-a"]
	proxied.URL = fault.URL
	faultEngine, err := NewEngine(proxied)
	if err != nil {
		t.Fatal(err)
	}
	// Verify this new connection against the same persisted node identity.
	c.engines = map[string]*Engine{"capacity-a": faultEngine, "capacity-b": engines["capacity-b"]}
	c.nodes = map[string]config.RuntimeNode{"capacity-a": proxied, "capacity-b": nodes["capacity-b"]}
	if err = c.SyncNodes(ctx); err != nil {
		t.Fatal(err)
	}
	if err = c.VirtualMachiner().Delete(ctx, &taskflow.DeleteVirtualMachineReq{ID: vm.ID, UserID: owner}); err == nil {
		t.Fatal("discarded removal reply acknowledged as complete")
	}
	if count, _, _ := capacityUsage(t, l); count != 1 {
		t.Fatal("unconfirmed removal released capacity")
	}
	rejected()
	if err = c.VirtualMachiner().Resume(ctx, &taskflow.ResumeVirtualMachineReq{ID: vm.ID, UserID: owner}); err == nil {
		t.Fatal("resume crossed recycle fence")
	}
	c.engines["capacity-a"] = direct
	c.nodes["capacity-a"] = nodes["capacity-a"]
	if err = c.SyncNodes(ctx); err != nil {
		t.Fatal(err)
	}
	if err = c.VirtualMachiner().Delete(ctx, &taskflow.DeleteVirtualMachineReq{ID: vm.ID, UserID: owner}); err != nil {
		t.Fatal("same sandbox removal reconciliation failed", err)
	}
	if count, _, _ := capacityUsage(t, l); count != 0 {
		t.Fatal("confirmed not-found did not release")
	}
	replacement, err := c.VirtualMachiner().Create(ctx, &taskflow.CreateVirtualMachineReq{UserID: owner, HostID: "capacity-b", Cores: "1", Memory: 2 << 30, TaskID: uuid.New()})
	if err != nil {
		t.Fatal("released capacity not reusable", err)
	}
	if err = c.VirtualMachiner().Delete(ctx, &taskflow.DeleteVirtualMachineReq{ID: replacement.ID, UserID: owner}); err != nil {
		t.Fatal(err)
	}
	t.Log("actual successful Docker removal with lost reply retained reservation; retry confirmed absence, released atomically and admitted the other node")
}
