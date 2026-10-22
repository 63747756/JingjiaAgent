package workartifact

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

func saveConversionFixture(t *testing.T, s *Service, owner, task uuid.UUID, name, content string) ArtifactVersion {
	t.Helper()
	f, digest, size, err := spool(bytes.NewBufferString(content), 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer removeSpool(f)
	if err = s.saveArtifact(context.Background(), owner, storageJob{TaskID: task}, name, f, digest, size); err != nil {
		t.Fatal(err)
	}
	versions, err := s.Versions(context.Background(), owner, uuid.NewSHA1(task, []byte(name)))
	if err != nil || len(versions) == 0 {
		t.Fatalf("missing saved version: %v", err)
	}
	return versions[0]
}

func TestConvertTaskFinishesHistoricalNativePreviews(t *testing.T) {
	s, owner, _, _, task := testStorage(t)
	ctx := context.Background()
	first := saveConversionFixture(t, s, owner, task, "output/report.txt", "old report")
	latest := saveConversionFixture(t, s, owner, task, "output/report.txt", "new report")
	if err := s.convertTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	versions, err := s.Versions(ctx, owner, first.ArtifactID)
	if err != nil || len(versions) != 2 {
		t.Fatalf("unexpected versions: %v", err)
	}
	for _, version := range versions {
		if version.ValidationStatus != "passed" || version.PreviewStatus != "ready" {
			t.Fatalf("v%d was stranded: validation=%s preview=%s", version.Version, version.ValidationStatus, version.PreviewStatus)
		}
	}
	artifact, err := s.Artifact(ctx, owner, first.ArtifactID)
	if err != nil || artifact.CurrentVersionID != latest.ID {
		t.Fatalf("conversion replaced the current version: %v", err)
	}
}

func TestConvertTaskRetriesHistoricalOfficeVersionsAfterOutage(t *testing.T) {
	for _, format := range []string{"docx", "xlsx", "pptx", "pdf"} {
		t.Run(format, func(t *testing.T) {
			s, owner, _, _, task := testStorage(t)
			ctx := context.Background()
			var outage atomic.Bool
			outage.Store(true)
			// Only the external converter is simulated; persistence, conversion
			// retries, version promotion and download authorization are real.
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/convert" || r.URL.Query().Get("format") != format || r.Header.Get("Authorization") != "Bearer test-token" {
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				if outage.Load() {
					http.Error(w, "converter offline", http.StatusServiceUnavailable)
					return
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, "read error", http.StatusBadRequest)
					return
				}
				var data bytes.Buffer
				z := zip.NewWriter(&data)
				files := map[string][]byte{
					"preview.pdf":     append([]byte("%PDF-1.4\n"), raw...),
					"validation.json": []byte(`{"status":"passed"}`),
				}
				if format == "xlsx" {
					files["canonical.xlsx"] = append(raw, []byte("-recalculated")...)
				}
				for name, content := range files {
					file, err := z.Create(name)
					if err != nil {
						http.Error(w, "archive error", http.StatusInternalServerError)
						return
					}
					if _, err = file.Write(content); err != nil {
						http.Error(w, "archive error", http.StatusInternalServerError)
						return
					}
				}
				if err = z.Close(); err != nil {
					http.Error(w, "archive error", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/zip")
				_, _ = w.Write(data.Bytes())
			}))
			defer server.Close()
			tokenFile := filepath.Join(t.TempDir(), "converter-token")
			if err := os.WriteFile(tokenFile, []byte("test-token"), 0600); err != nil {
				t.Fatal(err)
			}
			s.cfg.Work.ConverterURL, s.cfg.Work.ConverterTokenFile = server.URL, tokenFile
			name := "output/report." + format
			first := saveConversionFixture(t, s, owner, task, name, "original")
			if err := s.convertTask(ctx, task); err == nil {
				t.Fatal("converter outage should remain retryable")
			}
			latest := saveConversionFixture(t, s, owner, task, name, "newer")
			outage.Store(false)
			// A newer round finishes first, before the failed old job retries.
			if err := s.convertVersion(ctx, latest); err != nil {
				t.Fatal(err)
			}
			current, err := s.Artifact(ctx, owner, first.ArtifactID)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.convertTask(ctx, task); err != nil {
				t.Fatal(err)
			}
			after, err := s.Artifact(ctx, owner, first.ArtifactID)
			if err != nil || after.CurrentVersionID != current.CurrentVersionID {
				t.Fatalf("historical conversion rolled current back: %v", err)
			}
			page, err := s.ListArtifacts(ctx, owner, nil, &task, ArtifactPageRequest{})
			if err != nil || len(page.Items) != 1 || page.Items[0].CurrentVersionID != current.CurrentVersionID {
				t.Fatalf("artifact list selected a historical derivative: %+v %v", page, err)
			}
			versions, err := s.Versions(ctx, owner, first.ArtifactID)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range versions {
				if v.ValidationStatus == "pending" || v.PreviewStatus != "ready" {
					t.Fatalf("v%d remains unfinished: validation=%s preview=%s", v.Version, v.ValidationStatus, v.PreviewStatus)
				}
			}
			stream, _, _, err := s.Download(ctx, owner, first.ArtifactID, first.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			preview, err := io.ReadAll(stream)
			stream.Close()
			if err != nil || string(preview) != "%PDF-1.4\noriginal" {
				t.Fatalf("historical preview does not match its source: %v", err)
			}
			stream, _, _, err = s.Download(ctx, owner, first.ArtifactID, first.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(stream)
			stream.Close()
			if err != nil || string(raw) != "original" {
				t.Fatalf("historical original was modified: %v", err)
			}
			stream, _, _, err = s.Download(ctx, owner, first.ArtifactID, uuid.Nil, false)
			if err != nil {
				t.Fatal(err)
			}
			raw, err = io.ReadAll(stream)
			stream.Close()
			expected := "newer"
			if format == "xlsx" {
				expected += "-recalculated"
			}
			if err != nil || string(raw) != expected {
				t.Fatalf("default download rolled back to a historical version: %q %v", raw, err)
			}
			if format == "xlsx" {
				// A stale worker can finish twice after losing its lease. There
				// must be just one canonical derivative per immutable source.
				if err = s.convertVersion(ctx, first); err != nil {
					t.Fatal(err)
				}
				var count int
				if err = s.db.QueryRow(`SELECT count(*) FROM work_artifact_versions WHERE source_version_id=$1`, first.ID).Scan(&count); err != nil || count != 1 {
					t.Fatalf("retry duplicated the canonical version: count=%d %v", count, err)
				}
			}
			var delivery string
			if err = s.db.QueryRow(`SELECT delivery_status FROM work_tasks WHERE task_id=$1`, task).Scan(&delivery); err != nil || delivery != "delivered" {
				t.Fatalf("historical validation changed current delivery state: %s %v", delivery, err)
			}
		})
	}
}
