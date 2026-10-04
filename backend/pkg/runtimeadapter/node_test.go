package runtimeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"github.com/google/uuid"
)

type nodeRegistryFixture struct {
	mu    sync.Mutex
	count int
	fail  bool
	actor string
}

type blockedNodeProjectFixture struct {
	rpc.UnimplementedProjectServiceHandler
	started chan struct{}
}

func (s *blockedNodeProjectFixture) ApplyProject(ctx context.Context, _ *connect.Request[v2.ApplyProjectRequest]) (*connect.Response[v2.ApplyProjectResponse], error) {
	close(s.started)
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestNodeHeartbeatContinuesWhileWorkerRPCIsBlocked(t *testing.T) {
	l := testLedger(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &blockedNodeProjectFixture{started: make(chan struct{})}
	mux := http.NewServeMux()
	path, handler := rpc.NewProjectServiceHandler(s)
	mux.Handle(path, handler)
	var queries atomic.Int32
	beat := make(chan struct{}, 1)
	snapshot := validNodeSnapshot()
	mux.HandleFunc("/internal/monkeycode/node", func(w http.ResponseWriter, _ *http.Request) {
		if queries.Add(1) >= 3 {
			select {
			case beat <- struct{}{}:
			default:
			}
		}
		_ = json.NewEncoder(w).Encode(snapshot)
	})
	c := &Client{ledger: l, registry: &nodeRegistryFixture{}, backend: "agent_compose", poll: 100 * time.Millisecond,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), nodes: map[string]config.RuntimeNode{"node": {ID: "node", GuestImage: "fixture-image"}}, engines: map[string]*Engine{"node": testEngine(t, mux)}}
	if err := c.SyncNodes(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.VirtualMachiner().Create(ctx, &taskflow.CreateVirtualMachineReq{ID: "env", HostID: "node", UserID: uuid.NewString(), Cores: "1", Memory: 2 << 30}); err != nil {
		t.Fatal(err)
	}
	nodesDone, workerDone := make(chan error, 1), make(chan error, 1)
	go func() { nodesDone <- c.runNodes(ctx) }()
	go func() { workerDone <- c.RunWorker(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-nodesDone:
		case <-time.After(5 * time.Second):
			t.Error("node loop did not stop")
		}
		select {
		case <-workerDone:
		case <-time.After(5 * time.Second):
			t.Error("worker loop did not stop")
		}
	})
	select {
	case <-s.started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker RPC did not begin")
	}
	select {
	case <-beat:
	case <-time.After(15 * time.Second):
		t.Fatal("heartbeat stalled behind worker RPC")
	}
}

func (r *nodeRegistryFixture) SyncRuntimeHost(context.Context, config.RuntimeNode, *taskflow.Host) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.count++
	if r.fail {
		return errors.New("private registry failure")
	}
	return nil
}
func (r *nodeRegistryFixture) ListRuntimeHosts(_ context.Context, actor string, ids []string) (map[string]*taskflow.Host, error) {
	if actor != r.actor {
		return map[string]*taskflow.Host{}, nil
	}
	out := map[string]*taskflow.Host{}
	for _, id := range ids {
		out[id] = &taskflow.Host{ID: id}
	}
	return out, nil
}
func validNodeSnapshot() NodeSnapshot {
	return NodeSnapshot{Schema: "monkeycode.runtime.node.v1", InstanceID: uuid.NewString(), Fingerprint: strings.Repeat("a", 64), Hostname: "real-host", Arch: "x86_64", OS: "linux", Version: "agent-compose-c03302d-p7", Cores: 4, Memory: 8 << 30, SampledAt: time.Now().Unix()}
}
func TestNodeMetadataTransportAndValidation(t *testing.T) {
	snapshot := validNodeSnapshot()
	mode := "valid"
	redirected := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer destination.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/monkeycode/node", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("node metadata request lacked authentication")
		}
		switch mode {
		case "redirect":
			http.Redirect(w, r, destination.URL, http.StatusFound)
		case "oversize":
			_, _ = w.Write([]byte(strings.Repeat("x", 65537)))
		case "unauthorized":
			http.Error(w, "private diagnostic", 401)
		case "deadline":
			<-r.Context().Done()
		default:
			_ = json.NewEncoder(w).Encode(snapshot)
		}
	})
	e := testEngine(t, mux)
	if got, err := e.nodeSnapshot(context.Background()); err != nil || got.InstanceID != snapshot.InstanceID {
		t.Fatalf("node metadata failed: %v", err)
	}
	for _, value := range []string{"redirect", "oversize", "unauthorized", "deadline"} {
		mode = value
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, err := e.nodeSnapshot(ctx)
		cancel()
		if err == nil || strings.Contains(err.Error(), "private diagnostic") {
			t.Fatal("unsafe node metadata error")
		}
	}
	if redirected {
		t.Fatal("node credential request followed redirect")
	}
	for _, change := range []func(*NodeSnapshot){
		func(s *NodeSnapshot) { s.InstanceID = uuid.Nil.String() }, func(s *NodeSnapshot) { s.Fingerprint = "bad" },
		func(s *NodeSnapshot) { s.OS = "windows" }, func(s *NodeSnapshot) { s.Memory = 1 << 63 },
		func(s *NodeSnapshot) { v := s.Memory + 1; s.MemoryAvailable = &v }, func(s *NodeSnapshot) { v := uint64(1); s.DiskAvailable = &v },
		func(s *NodeSnapshot) { s.SampledAt = time.Now().Add(-time.Hour).Unix() },
	} {
		copy := snapshot
		change(&copy)
		if copy.validate() == nil {
			t.Fatal("invalid node metadata accepted")
		}
	}
}

func TestNodeHeartbeatIdentityFreshnessAndAdmission(t *testing.T) {
	ctx := context.Background()
	l := testLedger(t)
	snapshot := validNodeSnapshot()
	mu := sync.Mutex{}
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/monkeycode/node", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(snapshot)
	})
	r := &nodeRegistryFixture{actor: uuid.NewString()}
	c := &Client{ledger: l, registry: r, backend: "agent_compose", nodes: map[string]config.RuntimeNode{"node": {ID: "node"}}, engines: map[string]*Engine{"node": testEngine(t, mux)}}
	online := func(want bool) {
		t.Helper()
		got, err := c.Host().IsOnline(ctx, &taskflow.IsOnlineReq[string]{IDs: []string{"node", "node"}})
		if err != nil || got.OnlineMap["node"] != want {
			t.Fatalf("node online=%v err=%v", got, err)
		}
	}
	request := &taskflow.CreateVirtualMachineReq{ID: "env", HostID: "node", UserID: r.actor, Cores: "1", Memory: 2 << 30}
	online(false)
	if _, err := c.VirtualMachiner().Create(ctx, request); err == nil {
		t.Fatal("unregistered node admitted environment")
	}
	if err := c.SyncNodes(ctx); err != nil {
		t.Fatal(err)
	}
	online(true)
	if _, err := c.VirtualMachiner().Create(ctx, request); err != nil {
		t.Fatal(err)
	}
	for actor, want := range map[string]int{r.actor: 1, uuid.NewString(): 0} {
		hosts, err := c.Host().List(ctx, actor)
		if err != nil || len(hosts) != want {
			t.Fatal("host visibility bypassed registry")
		}
	}
	if _, err := l.db.ExecContext(ctx, `UPDATE runtime_nodes SET last_seen_at=now()-interval '36 seconds'`); err != nil {
		t.Fatal(err)
	}
	online(false)
	if _, _, err := c.environment(ctx, "env"); err == nil {
		t.Fatal("stale node allowed workload operation")
	}
	if n, err := c.observationEngine(ctx, "node"); err != nil || n != nil {
		t.Fatal("stale node allowed status RPC")
	}
	if _, err := c.VirtualMachiner().Create(ctx, request); err != nil {
		t.Fatal("idempotent environment retry did not retain original node")
	}
	if err := c.SyncNodes(ctx); err != nil {
		t.Fatal(err)
	}
	online(true)
	mu.Lock()
	original := snapshot
	snapshot.InstanceID = uuid.NewString()
	mu.Unlock()
	if err := c.SyncNodes(ctx); err != nil {
		t.Fatal(err)
	}
	online(false)
	if r.count != 2 {
		t.Fatal("changed identity updated original host")
	}
	mu.Lock()
	snapshot = original
	mu.Unlock()
	if err := c.SyncNodes(ctx); err != nil {
		t.Fatal(err)
	}
	online(true)
	// Another configured alias must not claim this same persistent daemon.
	c.nodes["alias"] = config.RuntimeNode{ID: "alias"}
	c.engines["alias"] = c.engines["node"]
	if err := c.SyncNodes(ctx); err != nil {
		t.Fatal(err)
	}
	if yes, err := l.nodeOnline(ctx, "alias"); err != nil || yes {
		t.Fatal("duplicate daemon identity accepted")
	}
	r.fail = true
	if err := c.SyncNodes(ctx); err != nil {
		t.Fatal(err)
	}
	online(false)
	var recorded string
	if err := l.db.QueryRowContext(ctx, `SELECT instance_id FROM runtime_nodes WHERE node_id='node'`).Scan(&recorded); err != nil || recorded != original.InstanceID {
		t.Fatal("failed heartbeat erased node binding")
	}
	r.fail = false
	badSnapshot := validNodeSnapshot()
	badMux := http.NewServeMux()
	badMux.HandleFunc("/internal/monkeycode/node", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(badSnapshot) })
	bad := &Client{ledger: l, registry: r, nodes: map[string]config.RuntimeNode{"node": {ID: "node"}}, engines: map[string]*Engine{"node": testEngine(t, badMux)}}
	if err := bad.SyncNodes(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.SyncNodes(ctx); err != nil {
		t.Fatal(err)
	}
	online(true)
	other, err := bad.Host().IsOnline(ctx, &taskflow.IsOnlineReq[string]{IDs: []string{"node"}})
	if err != nil || other.OnlineMap["node"] {
		t.Fatal("another process heartbeat authorized this process's unverified endpoint")
	}
	if _, _, err := bad.environment(ctx, "env"); err == nil {
		t.Fatal("unverified process received existing environment work")
	}
	c.nodeStateMu.Lock()
	c.nodeVerified["node"] = time.Now().Add(-36 * time.Second)
	c.nodeStateMu.Unlock()
	online(false)
}
