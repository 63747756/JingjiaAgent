package runtimeadapter

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type discoveryExecFixture struct {
	rpc.UnimplementedExecServiceHandler
	ports   atomic.Value
	calls   atomic.Int32
	failing atomic.Bool
}

func (f *discoveryExecFixture) Exec(context.Context, *connect.Request[v2.ExecRequest]) (*connect.Response[v2.ExecResponse], error) {
	f.calls.Add(1)
	if f.failing.Load() {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("node unavailable"))
	}
	return connect.NewResponse(&v2.ExecResponse{Result: &v2.ExecResult{Stdout: f.ports.Load().(string)}}), nil
}
func discoveryFixture(t *testing.T, upstream string) (*Client, Environment, *Engine, *discoveryExecFixture) {
	t.Helper()
	exec := &discoveryExecFixture{}
	exec.ports.Store(`[{"port":8080,"process":"web"}]`)
	mux := http.NewServeMux()
	path, handler := rpc.NewExecServiceHandler(exec)
	mux.Handle(path, handler)
	installPreviewTunnel(t, mux, upstream)
	engine := testEngine(t, mux)
	c := &Client{preview: config.RuntimePreview{Mode: "domain", BaseURL: "https://preview.example.test"}}
	return c, Environment{ID: "discovery-env", SandboxID: "sandbox", State: "online"}, engine, exec
}
func expireDiscovery(c *Client) {
	c.previewDiscovery.mu.Lock()
	defer c.previewDiscovery.mu.Unlock()
	for _, entry := range c.previewDiscovery.entries {
		entry.completed = time.Now().Add(-6 * time.Second)
	}
}

func TestPreviewDiscoveryHTTPStatusAndProtectedPorts(t *testing.T) {
	for _, status := range []int{200, 301, 401, 404, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int32
			app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Location", "http://must-not-follow.invalid/")
				w.WriteHeader(status)
				w.(http.Flusher).Flush()
				// A detector must close after headers instead of downloading a
				// long streaming body (or following an external redirect).
				<-r.Context().Done()
			}))
			defer app.Close()
			c, env, engine, exec := discoveryFixture(t, app.URL)
			exec.ports.Store(`[{"port":8080,"process":"web"},{"port":9000,"protected":true}]`)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			got, err := c.discoverHTTPServices(ctx, env, engine)
			if err != nil || got.Uncertain || len(got.Services) != 1 || got.Services[0].Port != 8080 || requests.Load() != 1 {
				t.Fatalf("HTTP discovery: %+v requests=%d error=%v", got, requests.Load(), err)
			}
		})
	}
}

func TestPreviewDiscoveryCoalescesAndPreservesUnknown(t *testing.T) {
	var slow atomic.Bool
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slow.Load() {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(404)
	}))
	defer app.Close()
	c, env, engine, exec := discoveryFixture(t, app.URL)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := c.discoverHTTPServices(context.Background(), env, engine)
			if err != nil || len(got.Services) != 1 {
				t.Errorf("shared discovery: %+v %v", got, err)
			}
		}()
	}
	wg.Wait()
	if exec.calls.Load() != 1 {
		t.Fatal("concurrent calls did not share discovery", exec.calls.Load())
	}
	slow.Store(true)
	expireDiscovery(c)
	got, err := c.discoverHTTPServices(context.Background(), env, engine)
	if err != nil || !got.Uncertain || len(got.Services) != 1 || got.Services[0].State != "unknown" {
		t.Fatalf("timeout removed known HTTP listener: %+v %v", got, err)
	}
	exec.failing.Store(true)
	expireDiscovery(c)
	if _, err = c.discoverHTTPServices(context.Background(), env, engine); err == nil {
		t.Fatal("network error incorrectly became empty result")
	}
	exec.failing.Store(false)
	exec.ports.Store(`[]`)
	expireDiscovery(c)
	got, err = c.discoverHTTPServices(context.Background(), env, engine)
	if err != nil || got.Uncertain || len(got.Services) != 0 {
		t.Fatalf("stopped listener retained: %+v %v", got, err)
	}
}

func TestPreviewDiscoveryExcludesRedisProtocol(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				b := make([]byte, 2048)
				conn.Read(b)
				io.WriteString(conn, "-ERR unknown command 'GET'\r\n")
			}()
		}
	}()
	c, env, engine, _ := discoveryFixture(t, "http://"+listener.Addr().String())
	got, err := c.discoverHTTPServices(context.Background(), env, engine)
	if err != nil || got.Uncertain || len(got.Services) != 0 {
		t.Fatalf("Redis treated as webpage: %+v %v", got, err)
	}
}

func TestPreviewDiscoveryUnknownTCPDoesNotMarkWebpagesUnhealthy(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				// Real Redis can silently close an HTTP probe as a cross-protocol
				// attack. EOF alone cannot prove either HTTP or a stopped service.
				b := make([]byte, 2048)
				conn.Read(b)
			}()
		}
	}()
	c, env, engine, _ := discoveryFixture(t, "http://"+listener.Addr().String())
	got, err := c.discoverHTTPServices(context.Background(), env, engine)
	if err != nil || got.Uncertain || len(got.Services) != 0 || len(got.UncertainPorts) != 1 {
		t.Fatalf("unknown TCP service marked webpages unhealthy: %+v %v", got, err)
	}
	// The same transport failure must retain a previously verified webpage.
	got, err = c.scanHTTPServices(context.Background(), env, engine, serviceDiscovery{
		Services: []discoveredService{{detectedPort: detectedPort{Port: 8080}, State: "ready"}},
	})
	if err != nil || !got.Uncertain || len(got.Services) != 1 || got.Services[0].State != "unknown" {
		t.Fatalf("EOF removed known webpage: %+v %v", got, err)
	}
}

func TestPreviewDiscoveryRetainsStoredHTTPAfterCacheReset(t *testing.T) {
	var slow atomic.Bool
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slow.Load() {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(500)
	}))
	defer app.Close()
	_, _, engine, _ := discoveryFixture(t, app.URL)
	c, env, owner := previewFixture(t, engine)
	if _, err := c.RequestEnvironmentPreview(context.Background(), env, owner, 8080); err != nil {
		t.Fatal(err)
	}
	// No list has run on this backend, so its discovery cache is empty. A
	// route nevertheless proves this was previously a confirmed HTTP service.
	slow.Store(true)
	got, err := c.PortForwarder().List(context.Background(), taskflow.ListPortforwadReq{ID: env})
	if err != nil || !got.ScanComplete || got.Status != "unknown" || len(got.Ports) != 1 || got.Ports[0].DiscoveryState != "unknown" || len(got.UncertainPorts) != 1 || got.UncertainPorts[0] != 8080 {
		t.Fatalf("cache reset hid stored HTTP service: %+v %v", got, err)
	}
}

func TestPreviewDiscoveryBoundsConcurrentHTTPProbes(t *testing.T) {
	var active, peak, total atomic.Int32
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		total.Add(1)
		for previous := peak.Load(); current > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, current) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		active.Add(-1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer func() {
		unblock()
		app.Close()
	}()
	c, env, engine, exec := discoveryFixture(t, app.URL)
	exec.ports.Store(`[{"port":8080},{"port":8081},{"port":8082},{"port":8083},{"port":8084},{"port":8085},{"port":8086},{"port":8087}]`)
	type scanResult struct {
		services serviceDiscovery
		err      error
	}
	done := make(chan scanResult, 1)
	go func() {
		result, err := c.discoverHTTPServices(context.Background(), env, engine)
		done <- scanResult{result, err}
	}()
	// Hold all probes before response headers. A fifth observed request in
	// this interval would exceed the limit even if the scan later succeeds.
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-deadline.C:
			t.Fatalf("first probe batch did not fill: started=%d", total.Load())
		}
	}
	select {
	case <-started:
		t.Fatalf("more than four concurrent probes: active=%d peak=%d", active.Load(), peak.Load())
	case <-time.After(150 * time.Millisecond):
	}
	unblock()
	select {
	case result := <-done:
		if result.err != nil || result.services.Uncertain || len(result.services.Services) != 8 || total.Load() != 8 || peak.Load() != 4 || exec.calls.Load() != 1 {
			t.Fatalf("bounded discovery: %+v error=%v total=%d peak=%d scans=%d", result.services, result.err, total.Load(), peak.Load(), exec.calls.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("remaining probes did not complete after releasing first batch")
	}
}
