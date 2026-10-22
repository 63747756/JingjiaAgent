package workspacesnapshot

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWorkspaceRoundTripPreservesCodeAttachmentsAndOutputs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux runtime filesystem permissions and symlinks")
	}
	root := t.TempDir()
	files := map[string]string{
		"main.py":           "print('local uncommitted edit')\n",
		".gitignore":        "ignored/\n",
		"ignored/cache.txt": "user data must not be silently excluded",
		".jingjiaagent/attachments/task/round/1-source.txt": "original input",
		".jingjiaagent/work/draft.txt":                      "draft",
		".jingjiaagent/output/report.txt":                   "deliverable",
		"run.sh":                                            "#!/bin/sh\nprintf restored-ok",
	}
	for name, data := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0640); err != nil {
			t.Fatal(err)
		}
	}
	os.Chmod(filepath.Join(root, "run.sh"), 0750)
	os.Mkdir(filepath.Join(root, "empty"), 0750)
	if err := os.Symlink("main.py", filepath.Join(root, "source-link")); err != nil {
		t.Fatal(err)
	}
	// External links are preserved as link text, never read on the host.
	if err := os.Symlink("/does-not-exist/private", filepath.Join(root, "external-link")); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %v %s", args, e, b)
		}
		return string(b)
	}
	git(root, "init", "-q")
	git(root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.test", "add", "main.py")
	git(root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "fixture")
	os.WriteFile(filepath.Join(root, "main.py"), []byte("print('changed without push')\n"), 0640)
	before := git(root, "status", "--porcelain")
	commit := git(root, "rev-parse", "HEAD")
	var b bytes.Buffer
	if err := Export(context.Background(), root, "full-fixture", &b); err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Verify(context.Background(), z, "full-fixture"); err != nil {
		t.Fatal(err)
	}
	restored := t.TempDir()
	if err = Restore(context.Background(), z, "full-fixture", restored); err != nil {
		t.Fatal(err)
	}
	if got := git(restored, "status", "--porcelain"); got != before {
		t.Fatalf("Git working state changed: %q != %q", got, before)
	}
	if got := git(restored, "rev-parse", "HEAD"); got != commit {
		t.Fatal("Git history lost")
	}
	for name := range files {
		want, _ := os.ReadFile(filepath.Join(root, name))
		got, e := os.ReadFile(filepath.Join(restored, name))
		if e != nil || !bytes.Equal(want, got) {
			t.Fatalf("file lost: %s %v", name, e)
		}
	}
	if info, e := os.Stat(filepath.Join(restored, "run.sh")); e != nil || info.Mode().Perm() != 0750 {
		t.Fatal("executable permission lost", e)
	}
	if info, e := os.Stat(filepath.Join(restored, "empty")); e != nil || !info.IsDir() {
		t.Fatal("empty directory lost")
	}
	if target, e := os.Readlink(filepath.Join(restored, "source-link")); e != nil || target != "main.py" {
		t.Fatal("symlink lost", e)
	}
	if target, e := os.Readlink(filepath.Join(restored, "external-link")); e != nil || target != "/does-not-exist/private" {
		t.Fatal("external symlink changed", e)
	}
	if err := Restore(context.Background(), z, "full-fixture", restored); err == nil {
		t.Fatal("overwrote a nonempty destination")
	}
}

func TestFullArchiveRejectsTamperingAndLinkParents(t *testing.T) {
	for _, mode := range []string{"changed-hash", "traversal", "duplicate", "symlink-parent", "missing-member"} {
		t.Run(mode, func(t *testing.T) {
			data := []byte("content")
			h := sha256.Sum256(data)
			m := Manifest{Schema: Schema, CheckpointID: "fixture", SourceDir: "workspace", Files: []File{{Path: "workspace/a", Type: "file", Size: int64(len(data)), SHA256: hex.EncodeToString(h[:]), Mode: 0600}}}
			entries := map[string][]byte{"workspace/a": data}
			switch mode {
			case "changed-hash":
				entries["workspace/a"] = []byte("changed")
			case "traversal":
				m.Files[0].Path = "workspace/../outside"
				delete(entries, "workspace/a")
				entries[m.Files[0].Path] = data
			case "missing-member":
				delete(entries, "workspace/a")
			case "symlink-parent":
				m.Files[0].Type = "symlink"
				m.Files = append(m.Files, File{Path: "workspace/a/b", Type: "file", Size: int64(len(data)), SHA256: hex.EncodeToString(h[:]), Mode: 0600})
				entries["workspace/a/b"] = data
			}
			var b bytes.Buffer
			z := zip.NewWriter(&b)
			w, _ := z.Create("manifest.json")
			json.NewEncoder(w).Encode(m)
			for name, data := range entries {
				w, _ = z.Create(name)
				w.Write(data)
			}
			if mode == "duplicate" {
				w, _ = z.Create("workspace/a")
				w.Write(data)
			}
			z.Close()
			r, _ := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
			if _, err := Verify(context.Background(), r, "fixture"); err == nil {
				t.Fatal("invalid archive accepted")
			}
		})
	}
}

func TestFullExportDetectsChangedWorkspaceDigest(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "code.txt"), []byte("before"), 0600)
	export := func() []byte {
		t.Helper()
		h := sha256.New()
		if e := Export(context.Background(), root, "same-id", h); e != nil {
			t.Fatal(e)
		}
		return h.Sum(nil)
	}
	first := export()
	if !bytes.Equal(first, export()) {
		t.Fatal("stable workspace digest is not deterministic")
	}
	os.WriteFile(filepath.Join(root, "code.txt"), []byte("after"), 0600)
	if bytes.Equal(first, export()) {
		t.Fatal("changed workspace matched committed archive")
	}
}
