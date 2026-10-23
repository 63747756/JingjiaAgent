package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveredSandboxKeepsDurableFiles(t *testing.T) {
	data := t.TempDir()
	rel := "2026/10/22/" + strings.Repeat("a", 64)
	dir := filepath.Join(data, "runtime/sandboxes", rel)
	for _, part := range []string{"vm", "workspace", "home/.opencode/xdg-data/opencode", "state"} {
		if err := os.MkdirAll(filepath.Join(dir, part), 0700); err != nil {
			t.Fatal(err)
		}
	}
	prefix := "/data/sandboxes/" + rel + "/"
	state := map[string]any{"driver": "docker", "box_id": "old-container", "stopped_at": "2026-10-22T00:00:00Z", "image": "sha256:" + strings.Repeat("b", 64)}
	if err := writeJSON(filepath.Join(dir, "vm/runtime.json"), state); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "metadata.json"), map[string]any{"summary": map[string]string{"id": filepath.Base(dir), "driver": "docker", "workspace_path": prefix + "workspace", "vm_status": "RUNNING", "unknown_field": "kept"}}); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"version": 1, "driver": "docker", "mounts": []map[string]string{{"type": "bind", "hostPath": prefix + "workspace", "guestPath": "/workspace"}, {"type": "bind", "hostPath": prefix + "home/.opencode", "guestPath": "/root/.opencode"}}}
	if err := writeJSON(filepath.Join(dir, "vm/mount-manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "workspace/kept.txt")
	if err := os.WriteFile(marker, []byte("durable"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareRecoveredSandboxes(data); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "vm/runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	var restored map[string]any
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatal(err)
	}
	if restored["box_id"] != nil || restored["stopped_at"] != nil || restored["image"] != state["image"] {
		t.Fatal("incorrect runtime recreation state")
	}
	if b, err := os.ReadFile(marker); err != nil || string(b) != "durable" {
		t.Fatal("workspace changed")
	}
	meta, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
	if err != nil || !strings.Contains(string(meta), `"XDG_DATA_HOME"`) || !strings.Contains(string(meta), `"/root/.opencode/xdg-data"`) {
		t.Fatal("native session persistence not configured")
	}
	var metadata struct{ Summary map[string]string }
	if err := json.Unmarshal(meta, &metadata); err != nil || metadata.Summary["vm_status"] != "STOPPED" || metadata.Summary["unknown_field"] != "kept" {
		t.Fatal("recovery did not stop the persisted summary while preserving unknown fields")
	}
	// Summary repair also applies without an OpenCode database, including a
	// pending sandbox which daemon startup would otherwise mark interrupted.
	if err := os.Rename(filepath.Join(dir, "home/.opencode/xdg-data/opencode"), filepath.Join(dir, "home/.opencode/xdg-data/saved")); err != nil {
		t.Fatal(err)
	}
	metadata.Summary["vm_status"] = "PENDING"
	if err := writeJSON(filepath.Join(dir, "metadata.json"), map[string]any{"summary": metadata.Summary}); err != nil {
		t.Fatal(err)
	}
	if err := prepareRecoveredSandboxes(data); err != nil {
		t.Fatal(err)
	}
	meta, err = os.ReadFile(filepath.Join(dir, "metadata.json"))
	if err != nil || json.Unmarshal(meta, &metadata) != nil || metadata.Summary["vm_status"] != "STOPPED" {
		t.Fatal("pending sandbox without OpenCode state was not prepared for recovery")
	}
	if _, err := os.Stat(filepath.Join(dir, "vm/runtime.json.before-recovery")); err != nil {
		t.Fatal(err)
	}
	// A broken mount must stop recovery, rather than create an empty directory.
	manifest["mounts"] = []map[string]string{{"type": "bind", "hostPath": prefix + "missing", "guestPath": "/workspace"}}
	if err := writeJSON(filepath.Join(dir, "vm/mount-manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if err := prepareRecoveredSandboxes(data); err == nil {
		t.Fatal("missing restored mount accepted")
	}
	manifest["mounts"] = []map[string]string{{"type": "bind", "hostPath": "/outside/workspace", "guestPath": "/workspace"}}
	if err := writeJSON(filepath.Join(dir, "vm/mount-manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if err := prepareRecoveredSandboxes(data); err == nil {
		t.Fatal("external mount accepted")
	}
}
