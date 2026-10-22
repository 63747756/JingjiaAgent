package workartifact

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
)

func seedPageArtifact(t *testing.T, s *Service, owner, task uuid.UUID, at time.Time, versions int) Artifact {
	t.Helper()
	aid := uuid.New()
	name := aid.String() + ".txt"
	_, err := s.db.Exec(`INSERT INTO work_artifacts(id,owner_id,task_id,title,filename,source_path,created_at) VALUES($1,$2,$3,$4,$4,$4,$5)`, aid, owner, task, name, at)
	if err != nil {
		t.Fatal(err)
	}
	var latest uuid.UUID
	for n := 1; n <= versions; n++ {
		latest = uuid.New()
		_, err = s.db.Exec(`INSERT INTO work_artifact_versions(id,artifact_id,version,filename,mime_type,storage_key,sha256,size,validation_status,preview_status) VALUES($1,$2,$3,$4,'text/plain',$1::uuid::text,'test',3,'passed','ready')`, latest, aid, n, name)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.db.Exec(`UPDATE work_artifacts SET current_version_id=$2 WHERE id=$1`, aid, latest)
	if err != nil {
		t.Fatal(err)
	}
	return Artifact{ID: aid, CurrentVersionID: latest}
}

func TestArtifactPagesReadBeyond200AndSurviveBoundaryDeletion(t *testing.T) {
	s, owner, _, _, task := testStorage(t)
	ctx := context.Background()
	for _, direction := range []string{"up", "down", "up"} {
		data, err := os.ReadFile("../../migration/000047_artifact_pagination." + direction + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.Exec(string(data)); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 10, 19, 0, 0, 0, 123456000, time.UTC)
	for n := 0; n < 205; n++ {
		seedPageArtifact(t, s, owner, task, at, 1)
	}
	page, err := s.ListArtifacts(ctx, owner, nil, &task, ArtifactPageRequest{})
	if err != nil || len(page.Items) != 24 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("first page: %d %v", len(page.Items), err)
	}
	seen := map[uuid.UUID]bool{}
	for _, a := range page.Items {
		seen[a.ID] = true
	}
	// Cursor positions are values, not row lookups. Removing its original row
	// must not break the next page, and newly created files wait for refresh.
	if _, err = s.db.Exec(`UPDATE work_artifacts SET deleted_at=now() WHERE id=$1`, page.Items[23].ID); err != nil {
		t.Fatal(err)
	}
	newest := seedPageArtifact(t, s, owner, task, at.Add(time.Hour), 1)
	for page.HasMore {
		page, err = s.ListArtifacts(ctx, owner, nil, &task, ArtifactPageRequest{Cursor: page.NextCursor, Limit: 24})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 24 {
			t.Fatal("unbounded page")
		}
		for _, a := range page.Items {
			if seen[a.ID] || a.ID == newest.ID {
				t.Fatal("duplicate or shifted page")
			}
			seen[a.ID] = true
			if a.VersionCount != 1 || a.CurrentVersion == nil || len(a.Versions) != 0 {
				t.Fatal("list eagerly loaded history")
			}
			b, _ := json.Marshal(a)
			var wire map[string]any
			json.Unmarshal(b, &wire)
			if _, ok := wire["versions"]; ok {
				t.Fatal("list includes history payload")
			}
		}
	}
	if len(seen) != 205 || page.NextCursor != "" {
		t.Fatalf("old artifacts lost: %d", len(seen))
	}
	page, err = s.ListArtifacts(ctx, owner, nil, nil, ArtifactPageRequest{Limit: 24})
	if err != nil || page.Items[0].ID != newest.ID {
		t.Fatal("refresh did not include new artifact", err)
	}
}

type pageTaskGuard struct{ denied uuid.UUID }

func (g pageTaskGuard) CheckWorkTaskAccess(_ context.Context, _, task uuid.UUID, _ bool) error {
	if task == g.denied {
		return errcode.ErrForbidden
	}
	return nil
}

func TestArtifactPagesSkipRevokedCandidatesWithoutTruncating(t *testing.T) {
	s, owner, _, _, task := testStorage(t)
	ctx := context.Background()
	other := uuid.New()
	if _, err := s.db.Exec(`INSERT INTO tasks(id,user_id) VALUES($1,$2)`, other, owner); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	for n := 0; n < 30; n++ {
		seedPageArtifact(t, s, owner, task, at.Add(time.Hour), 1)
	}
	for n := 0; n < 5; n++ {
		seedPageArtifact(t, s, owner, other, at, 1)
	}
	s.guard = pageTaskGuard{denied: task}
	page, err := s.ListArtifacts(ctx, owner, nil, nil, ArtifactPageRequest{Limit: 2})
	if err != nil || len(page.Items) != 2 || !page.HasMore {
		t.Fatalf("denied rows truncated page: %+v %v", page, err)
	}
	for _, a := range page.Items {
		if a.TaskID != other {
			t.Fatal("revoked task leaked")
		}
	}
}

func TestArtifactPageOnlyCountsPublishedVersionsAndChecksScope(t *testing.T) {
	s, owner, reader, project, task := testStorage(t)
	ctx := context.Background()
	a := seedPageArtifact(t, s, owner, task, time.Now(), 3)
	var first uuid.UUID
	if err := s.db.QueryRow(`SELECT id FROM work_artifact_versions WHERE artifact_id=$1 AND version=1`, a.ID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := s.Publish(ctx, owner, a.ID, project, first); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListArtifacts(ctx, reader, &project, nil, ArtifactPageRequest{})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("published artifact missing", err)
	}
	item := page.Items[0]
	if item.VersionCount != 1 || item.CurrentVersionID != first || item.TaskID != uuid.Nil || item.CanEdit || len(item.Versions) != 0 {
		t.Fatal("private version or task leaked")
	}
	owned, err := s.ListArtifacts(ctx, owner, nil, &task, ArtifactPageRequest{})
	if err != nil || owned.Items[0].VersionCount != 3 || owned.Items[0].CurrentVersionID != a.CurrentVersionID {
		t.Fatal("owner summary incorrect", err)
	}
	history, err := s.Versions(ctx, reader, a.ID)
	if err != nil || len(history) != 1 || history[0].ID != first {
		t.Fatal("history authorization mismatch", err)
	}
	if _, err = s.ListArtifacts(ctx, reader, nil, &task, ArtifactPageRequest{}); err == nil {
		t.Fatal("reader accessed private task")
	}
	if _, err = s.db.Exec(`DELETE FROM team_members WHERE user_id=$1`, reader); err != nil {
		t.Fatal(err)
	}
	page, err = s.ListArtifacts(ctx, reader, nil, nil, ArtifactPageRequest{})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("revoked publication leaked", err)
	}
	if _, err = s.ListArtifacts(ctx, reader, &project, nil, ArtifactPageRequest{}); err == nil {
		t.Fatal("revoked project scope accepted")
	}
}

func TestArtifactCursorAndLimitsRejectMalformedValues(t *testing.T) {
	s, owner, _, _, _ := testStorage(t)
	for _, req := range []ArtifactPageRequest{{Limit: -1}, {Limit: 101}, {Cursor: "not-a-cursor"}, {Cursor: "e30"}, {Cursor: encodeArtifactCursor(artifactCursor{ID: uuid.New()})}} {
		t.Run(fmt.Sprint(req), func(t *testing.T) {
			if _, err := s.ListArtifacts(context.Background(), owner, nil, nil, req); err == nil {
				t.Fatal("invalid paging accepted")
			}
		})
	}
}
