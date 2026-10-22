package workartifact

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/google/uuid"
	"io"
	"os"
	"path"
	"testing"
	"time"
)

func archiveFixture(t *testing.T, id uuid.UUID, files map[string][]byte, change func(*workspaceManifest)) (*os.File, int64) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "checkpoint-*.zip")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	z := zip.NewWriter(f)
	m := workspaceManifest{Schema: "jingjiaagent.workspace.checkpoint.v1", CheckpointID: id.String(), SourceDir: "work/output", Files: []manifestFile{}}
	for name, data := range files {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(data); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(data)
		m.Files = append(m.Files, manifestFile{Path: name, Size: int64(len(data)), SHA256: hex.EncodeToString(h[:])})
	}
	if change != nil {
		change(&m)
	}
	w, err := z.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	w.Write(jsonBytes(m))
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return f, st.Size()
}
func TestCheckpointRejectsTamperingAndUnsafePaths(t *testing.T) {
	id := uuid.New()
	cases := []struct {
		name   string
		files  map[string][]byte
		change func(*workspaceManifest)
		good   bool
	}{
		{name: "valid", files: map[string][]byte{"work/data.json": []byte("{}"), "output/report.txt": []byte("报告")}, good: true},
		{name: "hash mismatch", files: map[string][]byte{"output/a.txt": []byte("good")}, change: func(m *workspaceManifest) { m.Files[0].SHA256 = hex.EncodeToString(bytes.Repeat([]byte{0}, 32)) }},
		{name: "missing file", files: map[string][]byte{"output/a.txt": []byte("good")}, change: func(m *workspaceManifest) { m.Files = nil }},
		{name: "escape", files: map[string][]byte{"output/../secret": []byte("bad")}},
		{name: "input not checkpointed", files: map[string][]byte{"input/private.txt": []byte("bad")}},
		{name: "file parent conflict", files: map[string][]byte{"work/a": []byte("bad"), "work/a/b": []byte("bad")}},
		{name: "wrong checkpoint", files: map[string][]byte{}, change: func(m *workspaceManifest) { m.CheckpointID = uuid.NewString() }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f, n := archiveFixture(t, id, test.files, test.change)
			_, _, err := verifyArchive(f, n, id, 100, 1000, 10)
			if (err == nil) != test.good {
				t.Fatalf("accept=%v wanted %v: %v", err == nil, test.good, err)
			}
		})
	}
	f, n := archiveFixture(t, id, map[string][]byte{"output/a": bytes.Repeat([]byte{1}, 101)}, nil)
	if _, _, err := verifyArchive(f, n, id, 100, 1000, 10); err == nil {
		t.Fatal("oversized file accepted")
	}
}

type memoryStore struct {
	objects map[string][]byte
	corrupt bool
}

func (m *memoryStore) DeleteObject(_ context.Context, key string) error {
	delete(m.objects, key)
	return nil
}

func (m *memoryStore) GetObject(_ context.Context, key string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(m.objects[key])), nil
}
func (m *memoryStore) PresignGet(_ context.Context, key string, _ time.Duration) (string, error) {
	return "http://storage.test/" + key, nil
}
func (m *memoryStore) PutFile(_ context.Context, prefix, name string, r io.Reader) error {
	b, err := io.ReadAll(r)
	if m.corrupt {
		b = append(b, 0)
	}
	m.objects[path.Join(prefix, name)] = b
	return err
}
func TestStorageAcknowledgementMustMatchActualBytes(t *testing.T) {
	store := &memoryStore{objects: map[string][]byte{}, corrupt: true}
	s := &Service{store: store}
	f, digest, n, err := spool(bytes.NewBufferString("deliverable"), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer removeSpool(f)
	if err = s.putVerified(context.Background(), "work-private/test", f, digest, n); err == nil {
		t.Fatal("corrupt storage acknowledgement accepted")
	}
}
func TestSimpleFileValidationInspectsContents(t *testing.T) {
	for _, test := range []struct {
		format string
		data   []byte
		good   bool
	}{{"json", []byte("{}"), true}, {"json", []byte("{"), false}, {"txt", []byte{0, 1}, false}, {"csv", []byte("a,b\n1,2\n"), true}, {"csv", []byte("\"unterminated"), false}, {"png", []byte("not an image"), false}, {"md", []byte("# 报告\n"), true},
		{"patch", []byte("diff --git a/main.py b/main.py\n"), true}, {"diff", []byte{0xff}, false},
		{"py", []byte("def add(a, b):\n    return a + b\n"), true}, {"ts", []byte{0}, false}, {"exe", []byte("arbitrary binary"), false}} {
		if got := validateSimpleFile(test.format, test.data); got != test.good {
			t.Fatalf("%s accepted=%v expected=%v", test.format, got, test.good)
		}
	}
}
