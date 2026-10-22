package proxy

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/workspaces"
	"github.com/chaitin/agent-compose/pkg/workspacesnapshot"
	containerapi "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/labstack/echo/v4"
)

// Opt-in: run in a dedicated test container with the Docker socket and an
// otherwise empty shared named volume mounted at /live-workspace.
func TestJingjiaAgentRunningCheckpointDockerLifecycle(t *testing.T) {
	if os.Getenv("JINGJIAAGENT_CHECKPOINT_DOCKER_TEST") != "1" {
		t.Skip("isolated Docker checkpoint fixture required")
	}
	volume := os.Getenv("JINGJIAAGENT_CHECKPOINT_VOLUME")
	if volume == "" {
		t.Fatal("shared fixture volume required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	root, err := os.MkdirTemp("/live-workspace", "checkpoint-live-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := os.MkdirAll(filepath.Join(root, ".jingjiaagent", "output"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".jingjiaagent", "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".jingjiaagent", "output", "report.txt"), []byte("preserved report"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("checkpoint-live-%d", time.Now().UnixNano())
	box, err := docker.ContainerCreate(ctx, &containerapi.Config{
		Image: "ubuntu:24.04", WorkingDir: root,
		Cmd:    []string{"sh", "-c", "while :; do printf 'tick\\n' >> .jingjiaagent/work/live.log; sleep 0.05; done"},
		Labels: map[string]string{"agent-compose.sandbox_id": id, "agent-compose.driver": "docker"},
	}, &containerapi.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: volume, Target: "/live-workspace"}}}, nil, nil, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = docker.ContainerRemove(cleanup, box.ID, containerapi.RemoveOptions{Force: true})
	}()
	if err := docker.ContainerStart(ctx, box.ID, containerapi.StartOptions{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if info, err := os.Stat(filepath.Join(root, ".jingjiaagent", "work", "live.log")); err == nil && info.Size() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	before, err := docker.ContainerInspect(ctx, box.ID)
	if err != nil {
		t.Fatal(err)
	}
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: id, Driver: "docker", VMStatus: domain.VMStatusRunning, WorkspacePath: root}}
	app := echo.New()
	RegisterJingjiaAgentWorkspace(app, checkpointStore{sandbox}, sandboxes.NewLifecycleLocks(), "fixture-token")
	snapshot := func(want int, layout ...string) []byte {
		t.Helper()
		query := "/jingjiaagent/workspace/export?sandbox_id=" + id + "&checkpoint_id=live-snapshot&mode=running"
		if len(layout) > 0 {
			query += "&layout=" + layout[0]
		}
		req := httptest.NewRequest(http.MethodGet, query, nil)
		req.Header.Set("Authorization", "Bearer fixture-token")
		res := httptest.NewRecorder()
		app.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("snapshot response=%d, want=%d: %s", res.Code, want, res.Body.String())
		}
		after, err := docker.ContainerInspect(ctx, box.ID)
		if err != nil || !after.State.Running || after.State.Paused || after.State.Pid != before.State.Pid || after.State.StartedAt != before.State.StartedAt {
			t.Fatalf("autosave stopped, restarted or left the live process paused: %v", err)
		}
		return res.Body.Bytes()
	}
	data := snapshot(200)
	archive := filepath.Join(t.TempDir(), "live.zip")
	if err := os.WriteFile(archive, data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if err := workspaces.ValidateCheckpointArchive(ctx, archive, "live-snapshot", hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(z.File) != 3 {
		t.Fatalf("missing stable report/process files: %v", err)
	}
	// A new request recovers a freeze whose previous handler was interrupted.
	if err := docker.ContainerPause(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	snapshot(200)
	// Export rejection also unfreezes the original process.
	if err := os.Symlink("/etc/passwd", filepath.Join(root, ".jingjiaagent", "work", "unsafe-link")); err != nil {
		t.Fatal(err)
	}
	snapshot(422)
	// The direct artifact transport never visits work/ or the repository, even
	// when those directories contain entries unsuitable for a partial ZIP.
	direct := snapshot(200, "artifacts-v1")
	if bytes.HasPrefix(direct, []byte("PK")) {
		t.Fatal("direct artifact save produced a ZIP")
	}
	reader, err := workspacesnapshot.ArtifactReader(bytes.NewReader(direct))
	if err != nil {
		t.Fatal(err)
	}
	part, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	var manifest workspacesnapshot.ArtifactsManifest
	if err = json.NewDecoder(part).Decode(&manifest); err != nil || len(manifest.Files) != 1 || manifest.Files[0].Path != "output/report.txt" {
		t.Fatal(manifest, err)
	}
	part, err = reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(part)
	if err != nil || string(contents) != "preserved report" {
		t.Fatal(string(contents), err)
	}
	if err = os.Symlink("/etc/passwd", filepath.Join(root, ".jingjiaagent/output/unsafe")); err != nil {
		t.Fatal(err)
	}
	snapshot(422, "artifacts-v1")
	t.Log("real Docker: direct artifact files transferred without a partial backup; working files excluded; PID/start time unchanged; failure unpaused")
	t.Log("real Docker: report/heartbeat checkpoint validated; PID and start time unchanged; prior pause and export failure both unpaused")
}

type recycleLiveStore struct {
	checkpointStore
	removed bool
}

func (s *recycleLiveStore) GetSandbox(_ context.Context, _ string) (*domain.Sandbox, error) {
	if s.removed {
		return nil, os.ErrNotExist
	}
	return s.sandbox, nil
}
func (s *recycleLiveStore) ListSandboxes(context.Context, domain.SandboxListOptions) (domain.SandboxListResult, error) {
	return domain.SandboxListResult{}, nil
}
func (s *recycleLiveStore) UpdateSandbox(_ context.Context, value *domain.Sandbox) error {
	s.sandbox = value
	return nil
}
func (s *recycleLiveStore) RemoveSandbox(_ context.Context, id string) error {
	if s.sandbox.Summary.ID != id || filepath.Base(s.sandbox.Summary.WorkspacePath) != "workspace" || filepath.Base(filepath.Dir(s.sandbox.Summary.WorkspacePath)) != id {
		return fmt.Errorf("invalid fixture workspace deletion")
	}
	if err := os.RemoveAll(s.sandbox.Summary.WorkspacePath); err != nil {
		return err
	}
	s.removed = true
	return nil
}

type recycleLiveRuntime struct {
	docker    *client.Client
	container string
}

func (r recycleLiveRuntime) StopSandboxVM(ctx context.Context, _ *domain.Sandbox) error {
	return r.docker.ContainerStop(ctx, r.container, containerapi.StopOptions{})
}
func (r recycleLiveRuntime) RemoveSandboxVM(ctx context.Context, _ *domain.Sandbox) error {
	return r.docker.ContainerRemove(ctx, r.container, containerapi.RemoveOptions{})
}

func TestJingjiaAgentProtectedRecycleDockerLifecycle(t *testing.T) {
	if os.Getenv("JINGJIAAGENT_CHECKPOINT_DOCKER_TEST") != "1" {
		t.Skip("isolated Docker recycle fixture required")
	}
	volume := os.Getenv("JINGJIAAGENT_CHECKPOINT_VOLUME")
	if volume == "" {
		t.Fatal("missing shared test volume")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	root, err := os.MkdirTemp("/live-workspace", "recycle-live-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	id := filepath.Base(root) + "-sandbox"
	workspace := filepath.Join(root, id, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, ".jingjiaagent", "output"), 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(workspace, ".jingjiaagent", "workspace-layout.json"), []byte("{}"), 0600)
	os.WriteFile(filepath.Join(workspace, ".jingjiaagent", "output", "report.txt"), []byte("saved report"), 0600)
	source := filepath.Join(workspace, "main.py")
	os.WriteFile(source, []byte("print('blank-task code')"), 0600)
	box, err := docker.ContainerCreate(ctx, &containerapi.Config{Image: "ubuntu:24.04", Cmd: []string{"sleep", "60"}, Labels: map[string]string{"agent-compose.sandbox_id": id, "agent-compose.driver": "docker"}}, &containerapi.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: volume, Target: "/live-workspace"}}}, nil, nil, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		clean, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = docker.ContainerRemove(clean, box.ID, containerapi.RemoveOptions{Force: true})
	}()
	if err := docker.ContainerStart(ctx, box.ID, containerapi.StartOptions{}); err != nil {
		t.Fatal(err)
	}
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: id, Driver: "docker", VMStatus: domain.VMStatusStopped, WorkspacePath: workspace, RuntimeRef: box.ID}}
	store := &recycleLiveStore{checkpointStore: checkpointStore{sandbox}}
	removal := &sandboxes.RemovalCoordinator{SandboxRoot: root, Store: store, Runtime: recycleLiveRuntime{docker, box.ID}, Locks: sandboxes.NewLifecycleLocks()}
	app := echo.New()
	RegisterJingjiaAgentRecycle(app, store, removal, "fixture-token")
	RegisterJingjiaAgentWorkspace(app, store, removal.Locks, "fixture-token")
	var archive []byte
	digest := strings.Repeat("0", 64)
	request := func(want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/jingjiaagent/workspace/recycle?sandbox_id="+id+"&checkpoint_id=full-save&expected_sha256="+digest, nil)
		req.Header.Set("Authorization", "Bearer fixture-token")
		resp := httptest.NewRecorder()
		app.ServeHTTP(resp, req)
		if resp.Code != want {
			t.Fatalf("recycle status=%d want=%d", resp.Code, want)
		}
	}
	request(409) // Stale stopped metadata cannot bypass real Docker inspection.
	if err := docker.ContainerStop(ctx, box.ID, containerapi.StopOptions{}); err != nil {
		t.Fatal(err)
	}
	request(409) // No confirmed full archive: source must be retained.
	if data, err := os.ReadFile(source); err != nil || string(data) != "print('blank-task code')" {
		t.Fatal("code lost", err)
	}
	if store.removed {
		t.Fatal("blocked recycle removed sandbox metadata")
	}
	attachment := filepath.Join(workspace, ".jingjiaagent", "attachments", "task", "round", "original.txt")
	os.MkdirAll(filepath.Dir(attachment), 0700)
	os.WriteFile(attachment, []byte("retained attachment"), 0600)
	snapshot := func() {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/jingjiaagent/workspace/export?sandbox_id="+id+"&checkpoint_id=full-save&layout=full-v1", nil)
		req.Header.Set("Authorization", "Bearer fixture-token")
		resp := httptest.NewRecorder()
		app.ServeHTTP(resp, req)
		if resp.Code != 200 {
			t.Fatal("full export failed", resp.Code, resp.Body.String())
		}
		archive = bytes.Clone(resp.Body.Bytes())
		h := sha256.Sum256(archive)
		digest = hex.EncodeToString(h[:])
	}
	snapshot()
	os.WriteFile(source, []byte("print('new changes after export')"), 0600)
	request(409) // A stale full snapshot cannot authorize deletion.
	snapshot()
	request(204)
	request(204) // Lost replies retry the permanent removal receipt.
	if !store.removed {
		t.Fatal("safe recycle did not remove sandbox")
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatal("old workspace was not reclaimed", err)
	}
	restored := filepath.Join(root, "restored")
	os.Mkdir(restored, 0700)
	z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	if err := workspacesnapshot.Restore(ctx, z, "full-save", restored); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"main.py": "print('new changes after export')", ".jingjiaagent/output/report.txt": "saved report", ".jingjiaagent/attachments/task/round/original.txt": "retained attachment"} {
		got, e := os.ReadFile(filepath.Join(restored, name))
		if e != nil || string(got) != want {
			t.Fatal("restored content mismatch", name, e)
		}
	}
	t.Log("real Docker: running process protected; full workspace exported; stale digest rejected; sandbox removed; code, attachments and deliverable restored at original paths")
}
