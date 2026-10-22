package workartifact

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"testing"

	"github.com/google/uuid"
)

func TestRestoreArchivePreservesExecutableFilesWithCanonicalSpreadsheet(t *testing.T) {
	s, owner, _, _, task := testStorage(t)
	ctx := context.Background()
	raw := saveConversionFixture(t, s, owner, task, "output/report.xlsx", "original spreadsheet")
	canonical := saveConversionFixture(t, s, owner, task, "output/report.xlsx", "recalculated spreadsheet")
	if _, err := s.db.ExecContext(ctx, `UPDATE work_artifact_versions SET source_version_id=$2,validation_status='passed' WHERE id=$1`, canonical.ID, raw.ID); err != nil {
		t.Fatal(err)
	}

	files := []struct {
		name    string
		content string
		mode    os.FileMode
		want    os.FileMode
	}{
		{"work/run.sh", "#!/bin/sh\necho checkpoint-ok\n", 0o755, 0o711},
		{"work/private.sh", "#!/bin/sh\necho private\n", 0o700, 0o700},
		{"output/run.sh", "#!/bin/sh\necho output\n", os.ModeSetuid | os.ModeSetgid | os.ModeSticky | 0o777, 0o711},
		{"work/plain.txt", "plain text", 0o666, 0o600},
		// Old checkpoints lost their execution metadata. Never guess it from
		// the extension or a shebang when rebuilding one of those archives.
		{"work/not-executable.sh", "#!/bin/sh\necho not executable\n", 0o600, 0o600},
		{"output/report.xlsx", "original spreadsheet", 0o644, 0o600},
	}
	id := uuid.New()
	m := workspaceManifest{Schema: "jingjiaagent.workspace.checkpoint.v1", CheckpointID: id.String(), SourceDir: "work/output"}
	var original bytes.Buffer
	writer := zip.NewWriter(&original)
	for _, file := range files {
		header := &zip.FileHeader{Name: file.name, Method: zip.Store}
		header.SetMode(file.mode)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, file.content); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(file.content))
		m.Files = append(m.Files, manifestFile{Path: file.name, Size: int64(len(file.content)), SHA256: hex.EncodeToString(digest[:])})
	}
	manifest, err := writer.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.Write(jsonBytes(m)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive, _, size, err := spool(bytes.NewReader(original.Bytes()), s.cfg.Work.ArchiveLimit())
	if err != nil {
		t.Fatal(err)
	}
	defer removeSpool(archive)
	m, z, err := verifyArchive(archive, size, id, s.cfg.Work.FileLimit(), s.cfg.Work.ArchiveLimit(), s.cfg.Work.FileCountLimit())
	if err != nil {
		t.Fatal(err)
	}
	restored, digest, size, err := s.restoreArchive(ctx, task, m, z)
	if err != nil {
		t.Fatal(err)
	}
	if restored == nil {
		t.Fatal("canonical spreadsheet did not trigger archive rebuilding")
	}
	defer removeSpool(restored)
	h := sha256.New()
	if _, err := io.Copy(h, restored); err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(h.Sum(nil)) != digest {
		t.Fatal("rebuilt archive hash mismatch")
	}
	_, z, err = verifyArchive(restored, size, id, s.cfg.Work.FileLimit(), s.cfg.Work.ArchiveLimit(), s.cfg.Work.FileCountLimit())
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]*zip.File{}
	for _, file := range z.File {
		entries[file.Name] = file
	}
	for _, file := range files {
		t.Run(file.name, func(t *testing.T) {
			entry := entries[file.name]
			if entry == nil {
				t.Fatal("file missing from rebuilt archive")
			}
			if entry.Mode() != file.want {
				t.Fatalf("rebuilt mode=%v, want %v", entry.Mode(), file.want)
			}
			stream, err := entry.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			content, err := io.ReadAll(stream)
			if err != nil {
				t.Fatal(err)
			}
			want := file.content
			if file.name == "output/report.xlsx" {
				want = "recalculated spreadsheet"
			}
			if string(content) != want {
				t.Fatalf("rebuilt content=%q, want %q", content, want)
			}
		})
	}
}

func TestLegacyCheckpointUpgradesLayoutWithoutChangingFilesOrIdentity(t *testing.T) {
	s, _, _, _, task := testStorage(t)
	id := uuid.New()
	files := map[string][]byte{"work/context.json": []byte("context"), "output/report.md": []byte("original report")}
	f, size := archiveFixture(t, id, files, nil)
	m, archive, err := verifyArchive(f, size, id, s.cfg.Work.FileLimit(), s.cfg.Work.ArchiveLimit(), s.cfg.Work.FileCountLimit())
	if err != nil {
		t.Fatal(err)
	}
	restored, _, size, err := s.restoreArchive(context.Background(), task, m, archive)
	if err != nil || restored == nil {
		t.Fatal("legacy layout was not upgraded", err)
	}
	defer removeSpool(restored)
	converted, reader, err := verifyArchive(restored, size, id, s.cfg.Work.FileLimit(), s.cfg.Work.ArchiveLimit(), s.cfg.Work.FileCountLimit())
	if err != nil || converted.Schema != "jingjiaagent.workspace.checkpoint.v2" {
		t.Fatal("new node layout was not declared", err)
	}
	for _, entry := range reader.File {
		if entry.Name == "manifest.json" {
			continue
		}
		stream, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(stream)
		stream.Close()
		if err != nil || !bytes.Equal(data, files[entry.Name]) {
			t.Fatal("legacy checkpoint content or logical path changed", entry.Name, err)
		}
	}
}
