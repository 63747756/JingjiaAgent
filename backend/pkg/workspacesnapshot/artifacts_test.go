package workspacesnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactsTransportContainsOnlyDeliverables(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string]string{"main.py": "code", ".git/HEAD": "git", ".jingjiaagent/work/draft.txt": "draft", ".jingjiaagent/attachments/task/input.csv": "input", ".jingjiaagent/output/报告.txt": "result"} {
		p := filepath.Join(root, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0700)
		os.WriteFile(p, []byte(data), 0600)
	}
	var out bytes.Buffer
	if err := ExportArtifacts(context.Background(), root, "round-id", &out); err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(out.Bytes(), []byte("PK")) {
		t.Fatal("artifact save created a ZIP")
	}
	r, err := ArtifactReader(&out)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	var m ArtifactsManifest
	if err = json.NewDecoder(p).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m.Schema != ArtifactsSchema || len(m.Files) != 1 || m.Files[0].Path != "output/报告.txt" {
		t.Fatal(m)
	}
	p, err = r.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(p)
	if err != nil || string(data) != "result" {
		t.Fatal(string(data), err)
	}
	if _, err = r.NextPart(); err != io.EOF {
		t.Fatal("unexpected extra file", err)
	}
}

func TestArtifactsTransportRejectsLinksWithoutReadingOutsideOutput(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".jingjiaagent/output"), 0700)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(outside, []byte("secret"), 0600)
	if err := os.Symlink(outside, filepath.Join(root, ".jingjiaagent/output/link.txt")); err != nil {
		t.Skip(err)
	}
	if err := ExportArtifacts(context.Background(), root, "round-id", io.Discard); err == nil {
		t.Fatal("followed output link")
	}
}
