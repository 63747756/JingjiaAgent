package workspaces

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/chaitin/agent-compose/pkg/workspacesnapshot"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sources"
)

func TestFullCheckpointHTTPRestoreKeepsRepositoryAndAttachmentPaths(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	files := map[string]string{"main.go": "package main", ".git/HEAD": "ref: refs/heads/feature\n", ".jingjiaagent/output/report.txt": "report", ".jingjiaagent/attachments/task/round/file.txt": "attachment"}
	for name, data := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var data bytes.Buffer
	if err := workspacesnapshot.Export(ctx, root, "full-http", &data); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data.Bytes())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data.Bytes()) }))
	defer server.Close()
	config, err := NewHTTPWorkspaceConfig("restore", "restore", "", sources.Source{Provider: "http", Format: "zip", URL: server.URL + "#jingjiaagent-checkpoint=full-http&expected_sha256=" + hex.EncodeToString(digest[:])}, ".")
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := (httpWorkspace{workspace: config, limits: DefaultHTTPWorkspaceLimits()}).Prepare(ctx, &domain.Sandbox{Summary: domain.SandboxSummary{ID: "restore-full", WorkspacePath: destination}}); err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(destination, name))
		if err != nil || string(got) != want {
			t.Fatal("original path/content not restored", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(destination, "workspace")); !os.IsNotExist(err) {
		t.Fatal("archive namespace leaked into workspace")
	}
}

func checkpointFixture(t *testing.T) ([]byte, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".jingjiaagent", "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".jingjiaagent", "work", "context.json"), []byte(`{"goal":"办公任务"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".jingjiaagent", "output"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".jingjiaagent", "output", "报告.docx"), []byte("document"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "credential.env"), []byte("must not export"), 0o600); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := ExportCheckpoint(context.Background(), root, "checkpoint-1", &data); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data.Bytes())
	return data.Bytes(), hex.EncodeToString(hash[:])
}
func TestJingjiaAgentCheckpointRoundTripAndIntegrity(t *testing.T) {
	data, hash := checkpointFixture(t)
	archive := filepath.Join(t.TempDir(), "checkpoint.zip")
	if err := os.WriteFile(archive, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCheckpointArchive(context.Background(), archive, "checkpoint-1", hash); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCheckpointArchive(context.Background(), archive, "other", hash); err == nil {
		t.Fatal("accepted wrong checkpoint ID")
	}
	if err := ValidateCheckpointArchive(context.Background(), archive, "checkpoint-1", strings.Repeat("0", 64)); err == nil {
		t.Fatal("accepted wrong archive hash")
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 3 {
		t.Fatalf("exported unexpected files: %v", reader.File)
	}
	for _, attack := range []string{"../escape", "work/../credential.env", "output\\escape", "output/link", "output/extra.txt", "work/context.json"} {
		t.Run(attack, func(t *testing.T) {
			var changed bytes.Buffer
			writer := zip.NewWriter(&changed)
			for _, file := range reader.File {
				source, _ := file.Open()
				entry, _ := writer.Create(file.Name)
				_, err := io.Copy(entry, source)
				source.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			header := &zip.FileHeader{Name: attack, Method: zip.Store}
			header.SetMode(0o600)
			if attack == "output/link" {
				header.SetMode(os.ModeSymlink | 0o777)
			}
			entry, _ := writer.CreateHeader(header)
			entry.Write([]byte("malicious"))
			writer.Close()
			os.WriteFile(archive, changed.Bytes(), 0o600)
			digest := sha256.Sum256(changed.Bytes())
			if err := ValidateCheckpointArchive(context.Background(), archive, "checkpoint-1", hex.EncodeToString(digest[:])); err == nil {
				t.Fatal("accepted unsafe/extra/duplicate entry")
			}
		})
	}
}
func TestJingjiaAgentCheckpointRejectsLink(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".jingjiaagent", "work"), 0o700)
	if err := os.Symlink(t.TempDir(), filepath.Join(root, ".jingjiaagent", "work", "link")); err != nil {
		t.Skip("symlink unavailable")
	}
	var out bytes.Buffer
	if ExportCheckpoint(context.Background(), root, "check", &out) == nil {
		t.Fatal("accepted link")
	}
}
func TestJingjiaAgentCheckpointPrepareBeforeGuest(t *testing.T) {
	data, hash := checkpointFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Write(data)
	}))
	defer server.Close()
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "valid", false: "corrupt"}[valid], func(t *testing.T) {
			expected := hash
			if !valid {
				expected = strings.Repeat("0", 64)
			}
			config, err := NewHTTPWorkspaceConfig("restore", "restore", "", sources.Source{Provider: "http", Format: "zip", URL: server.URL + "#jingjiaagent-checkpoint=checkpoint-1&expected_sha256=" + expected}, ".")
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeHTTPWorkspaceConfig(config.ConfigJSON)
			if err != nil || decoded.URL != server.URL || decoded.ExpectedSHA256 != expected {
				t.Fatal("fragment metadata not materialized", err)
			}
			root := t.TempDir()
			workspace := httpWorkspace{workspace: config, limits: DefaultHTTPWorkspaceLimits()}
			err = workspace.Prepare(context.Background(), &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox", WorkspacePath: root}})
			if valid {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(root, ".jingjiaagent", "output", "报告.docx")); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(root, "manifest.json")); !os.IsNotExist(err) {
					t.Fatal("restored manifest into user workspace")
				}
			} else {
				if err == nil {
					t.Fatal("accepted corrupt checkpoint")
				}
				entries, _ := os.ReadDir(root)
				if len(entries) != 0 {
					t.Fatal("copied files before integrity check")
				}
			}
		})
	}
}

func TestJingjiaAgentCheckpointPreservesExecutableFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix file permissions and /bin/sh")
	}
	ctx := context.Background()
	root := t.TempDir()
	script := []byte("#!/bin/sh\nprintf 'checkpoint-ok\\n'\n")
	files := []struct {
		name string
		mode os.FileMode
		want os.FileMode
	}{
		{"work/run.sh", 0o755, 0o711},
		{"work/private.sh", 0o700, 0o700},
		{"output/run.sh", os.ModeSetuid | os.ModeSetgid | os.ModeSticky | 0o777, 0o711},
		{"work/plain.txt", 0o666, 0o600},
		{"work/not-executable.sh", 0o600, 0o600},
	}
	for _, file := range files {
		name := filepath.Join(root, ".jingjiaagent", file.name)
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, script, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(name, file.mode); err != nil {
			t.Fatal(err)
		}
	}
	var data bytes.Buffer
	if err := ExportCheckpoint(ctx, root, "executable-checkpoint", &data); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data.Bytes())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(data.Bytes())
	}))
	defer server.Close()
	config, err := NewHTTPWorkspaceConfig("restore", "restore", "", sources.Source{
		Provider: "http", Format: "zip",
		URL: server.URL + "#jingjiaagent-checkpoint=executable-checkpoint&expected_sha256=" + hex.EncodeToString(digest[:]),
	}, ".")
	if err != nil {
		t.Fatal(err)
	}
	restored := t.TempDir()
	workspace := httpWorkspace{workspace: config, limits: DefaultHTTPWorkspaceLimits()}
	if err := workspace.Prepare(ctx, &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox", WorkspacePath: restored}}); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		t.Run(file.name, func(t *testing.T) {
			name := filepath.Join(restored, ".jingjiaagent", file.name)
			info, err := os.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode() != file.want {
				t.Fatalf("restored mode=%v, want %v", info.Mode(), file.want)
			}
			content, err := os.ReadFile(name)
			if err != nil || !bytes.Equal(content, script) {
				t.Fatalf("restored content changed: %v", err)
			}
			if file.want&0o100 != 0 {
				out, err := exec.CommandContext(ctx, name).CombinedOutput()
				if err != nil || string(out) != "checkpoint-ok\n" {
					t.Fatalf("restored script cannot run: %q %v", out, err)
				}
			}
		})
	}
}

func TestJingjiaAgentCheckpointExportsOnlyManagedWorkProducts(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"work": "repository file", "output": "repository file", "input": "repository file", "project": "repository file",
		".jingjiaagent/work/process.json": "process", ".jingjiaagent/output/report.md": "report",
		".jingjiaagent/attachments/private.txt": "must not export", ".jingjiaagent/workspace-layout.json": "must not export",
	} {
		filename := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := ExportCheckpoint(context.Background(), root, "managed-layout", &out); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"manifest.json": true, "work/process.json": true, "output/report.md": true}
	for _, file := range reader.File {
		if !want[file.Name] {
			t.Fatalf("unexpected archived file: %s", file.Name)
		}
		delete(want, file.Name)
	}
	if len(want) != 0 {
		t.Fatalf("managed files missing: %v", want)
	}
	for _, name := range []string{"work", "output", "input", "project"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != "repository file" {
			t.Fatal("repository file changed", err)
		}
	}
}

func TestJingjiaAgentIdleLegacyWorkspaceIsNotArchivedAsEmpty(t *testing.T) {
	root := t.TempDir()
	for name, text := range map[string]string{"work/context.json": "legacy process", "output/report.md": "legacy report", ".jingjiaagent/attachments/file.txt": "attachment"} {
		filename := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var data bytes.Buffer
	if err := ExportCheckpoint(context.Background(), root, "legacy-idle", &data); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(data.Bytes()), int64(data.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 3 {
		t.Fatalf("legacy snapshot was lost: %v", reader.File)
	}
	for _, file := range reader.File {
		if file.Name != "manifest.json" && file.Name != "work/context.json" && file.Name != "output/report.md" {
			t.Fatalf("unexpected legacy snapshot member: %s", file.Name)
		}
	}
}

func TestJingjiaAgentLegacyV1CheckpointRestoresUnderManagedRoot(t *testing.T) {
	data, _ := checkpointFixture(t)
	original, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var legacy bytes.Buffer
	writer := zip.NewWriter(&legacy)
	for _, entry := range original.File {
		source, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(source)
		source.Close()
		if err != nil {
			t.Fatal(err)
		}
		if entry.Name == "manifest.json" {
			var manifest CheckpointManifest
			if err := json.Unmarshal(body, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest.Schema = LegacyCheckpointSchema
			body, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
		}
		header := entry.FileHeader
		target, err := writer.CreateHeader(&header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := target.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(legacy.Bytes())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(legacy.Bytes()) }))
	defer server.Close()
	cfg, err := NewHTTPWorkspaceConfig("restore", "restore", "", sources.Source{Provider: "http", Format: "zip", URL: server.URL + "#jingjiaagent-checkpoint=checkpoint-1&expected_sha256=" + hex.EncodeToString(digest[:])}, ".")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := (httpWorkspace{workspace: cfg, limits: DefaultHTTPWorkspaceLimits()}).Prepare(context.Background(), &domain.Sandbox{Summary: domain.SandboxSummary{ID: "legacy-restore", WorkspacePath: root}}); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(root, ".jingjiaagent", "output", "报告.docx")); err != nil || string(body) != "document" {
		t.Fatal("legacy report did not restore", err)
	}
	if _, err := os.Stat(filepath.Join(root, "output")); !os.IsNotExist(err) {
		t.Fatal("restored platform files into repository root")
	}
}
