package workartifact

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/oss"
	"github.com/63747756/jingjiaagent/backend/pkg/workspacesnapshot"
	"github.com/google/uuid"
)

func retainedFixture(t *testing.T, s *Service, task uuid.UUID, env string, full bool, at time.Time) (uuid.UUID, string) {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	if _, err := s.db.Exec(`INSERT INTO runtime_environments(id) VALUES($1) ON CONFLICT DO NOTHING`, env); err != nil {
		t.Fatal(err)
	}
	var f *os.File
	var size int64
	if full {
		root := t.TempDir()
		for name, data := range map[string]string{"main.py": "code", ".git/HEAD": "git", ".jingjiaagent/attachments/input.txt": "attachment", ".jingjiaagent/work/draft.txt": "draft"} {
			p := filepath.Join(root, filepath.FromSlash(name))
			os.MkdirAll(filepath.Dir(p), 0700)
			os.WriteFile(p, []byte(data), 0600)
		}
		var b bytes.Buffer
		if err := workspacesnapshot.Export(ctx, root, id.String(), &b); err != nil {
			t.Fatal(err)
		}
		var err error
		f, _, size, err = spool(&b, workspacesnapshot.MaxArchiveBytes)
		if err != nil {
			t.Fatal(err)
		}
		defer removeSpool(f)
	} else {
		f, size = archiveFixture(t, id, map[string][]byte{"work/draft.txt": []byte("old draft")}, nil)
	}
	manifest, _, err := verifyArchive(f, size, id, 100<<20, 256<<20, 2000)
	if err != nil {
		t.Fatal(err)
	}
	f.Seek(0, 0)
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	file, digest, n, err := spool(bytes.NewReader(data), workspacesnapshot.MaxArchiveBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer removeSpool(file)
	key := fmt.Sprintf("work-private/checkpoints/%s/%s.zip", task, id)
	if err = s.putVerified(ctx, key, file, digest, n); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO workspace_checkpoints(id,task_id,environment_id,state,archive_key,payload_sha256,size,manifest,created_at,committed_at) VALUES($1,$2,$3,'committed',$4,$5,$6,$7,$8,$8)`, id, task, env, key, digest, n, string(jsonBytes(manifest)), at); err != nil {
		t.Fatal(err)
	}
	return id, key
}

func assertCheckpointState(t *testing.T, s *Service, id uuid.UUID, want string) {
	t.Helper()
	var state string
	if err := s.db.QueryRow(`SELECT state FROM workspace_checkpoints WHERE id=$1`, id).Scan(&state); err != nil || state != want {
		t.Fatal("checkpoint state", state, want, err)
	}
}

func TestRetentionKeepsLatestFullAndPreservesArtifactsUploadsAndNewerLegacyProgress(t *testing.T) {
	s, owner, _, _, task := testStorage(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour)
	old, oldKey := retainedFixture(t, s, task, "env", true, at)
	partial, partialKey := retainedFixture(t, s, task, "env", false, at.Add(time.Minute))
	latest, latestKey := retainedFixture(t, s, task, "env", true, at.Add(2*time.Minute))
	newerPartial, newerPartialKey := retainedFixture(t, s, task, "env", false, at.Add(3*time.Minute))
	store := s.store.(*memoryStore)
	store.objects["temp/upload.xlsx"] = []byte("original")
	first := saveConversionFixture(t, s, owner, task, "output/report.txt", "first")
	second := saveConversionFixture(t, s, owner, task, "output/report.txt", "second")
	if err := s.PruneCheckpoints(ctx, task); err != nil {
		t.Fatal(err)
	}
	assertCheckpointState(t, s, old, "pruned")
	assertCheckpointState(t, s, partial, "pruned")
	assertCheckpointState(t, s, latest, "committed")
	assertCheckpointState(t, s, newerPartial, "committed")
	for _, key := range []string{oldKey, partialKey} {
		if _, ok := store.objects[key]; ok {
			t.Fatal("old archive retained", key)
		}
	}
	for _, key := range []string{latestKey, newerPartialKey, "temp/upload.xlsx", first.StorageKey, second.StorageKey} {
		if _, ok := store.objects[key]; !ok {
			t.Fatal("nonobsolete object deleted", key)
		}
	}
	versions, err := s.Versions(ctx, owner, first.ArtifactID)
	if err != nil || len(versions) != 2 {
		t.Fatal("artifact versions pruned", versions, err)
	}
	_, nextKey := retainedFixture(t, s, task, "env", true, at.Add(4*time.Minute))
	if err = s.PruneCheckpoints(ctx, task); err != nil {
		t.Fatal(err)
	}
	assertCheckpointState(t, s, latest, "pruned")
	assertCheckpointState(t, s, newerPartial, "pruned")
	if _, ok := store.objects[nextKey]; !ok {
		t.Fatal("latest full deleted")
	}
}

func TestRetentionWaitsForSuccessfulCommitAndDoesNotDropRestorePins(t *testing.T) {
	s, _, _, _, task := testStorage(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour)
	old, key := retainedFixture(t, s, task, "env", true, at)
	newID := uuid.New()
	if _, err := s.db.Exec(`INSERT INTO workspace_checkpoints(id,task_id,environment_id,state) VALUES($1,$2,'env','pending')`, newID, task); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneCheckpoints(ctx, task); err != nil {
		t.Fatal(err)
	}
	assertCheckpointState(t, s, old, "committed")
	latest, _ := retainedFixture(t, s, task, "env", true, at.Add(time.Minute))
	unlock, _, err := s.checkpointLock(ctx, task, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PruneCheckpoints(ctx, task); err != nil {
		t.Fatal(err)
	}
	assertCheckpointState(t, s, old, "committed")
	unlock()
	if _, err = s.db.Exec(`INSERT INTO runtime_restores(task_id,old_environment_id,new_environment_id,checkpoint_id,archive_key,payload_sha256,state) VALUES($1,'env','env',$2,$3,$4,'preparing')`, task, old, key, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err = s.PruneCheckpoints(ctx, task); err != nil {
		t.Fatal(err)
	}
	assertCheckpointState(t, s, old, "committed")
	assertCheckpointState(t, s, latest, "committed")
	if _, err = s.db.Exec(`UPDATE runtime_restores SET state='complete' WHERE task_id=$1`, task); err != nil {
		t.Fatal(err)
	}
	if err = s.PruneCheckpoints(ctx, task); err != nil {
		t.Fatal(err)
	}
	assertCheckpointState(t, s, old, "pruned")
}

type lostDeleteReplyStore struct {
	*memoryStore
	lost bool
}

func (s *lostDeleteReplyStore) DeleteObject(ctx context.Context, key string) error {
	s.memoryStore.DeleteObject(ctx, key)
	if !s.lost {
		s.lost = true
		return errors.New("lost delete reply")
	}
	return nil
}
func TestRetentionDeletionFailureIsDurableAndIdempotent(t *testing.T) {
	s, _, _, _, task := testStorage(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour)
	old, _ := retainedFixture(t, s, task, "env", true, at)
	latest, key := retainedFixture(t, s, task, "env", true, at.Add(time.Minute))
	store := s.store.(*memoryStore)
	s.store = &lostDeleteReplyStore{memoryStore: store}
	if err := s.PruneCheckpoints(ctx, task); err == nil {
		t.Fatal("delete failure ignored")
	}
	assertCheckpointState(t, s, old, "pruning")
	assertCheckpointState(t, s, latest, "committed")
	if err := s.PruneCheckpoints(ctx, task); err != nil {
		t.Fatal(err)
	}
	assertCheckpointState(t, s, old, "pruned")
	if _, ok := store.objects[key]; !ok {
		t.Fatal("new backup lost on old delete failure")
	}
}

func TestRetentionRemovesActualS3ObjectsAndKeepsHistory(t *testing.T) {
	endpoint := os.Getenv("JINGJIAAGENT_STORAGE_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("isolated MinIO required")
	}
	s, owner, _, _, task := testStorage(t)
	ctx := context.Background()
	store, err := oss.NewS3Compatible(ctx, config.ObjectStorageConfig{Endpoint: endpoint, AccessKey: "fixtureuser", AccessKeySecret: "fixture-password-only", Bucket: "retention-" + uuid.NewString()}, oss.S3Option{ForcePathStyle: true, InitBucket: true})
	if err != nil {
		t.Fatal(err)
	}
	s.store = store
	old, oldKey := retainedFixture(t, s, task, "env", true, time.Now().Add(-time.Minute))
	latest, key := retainedFixture(t, s, task, "env", true, time.Now())
	v := saveConversionFixture(t, s, owner, task, "output/report.txt", "old result")
	saveConversionFixture(t, s, owner, task, "output/report.txt", "new result")
	if err = s.PruneCheckpoints(ctx, task); err != nil {
		t.Fatal(err)
	}
	assertCheckpointState(t, s, old, "pruned")
	assertCheckpointState(t, s, latest, "committed")
	if r, err := store.GetObject(ctx, oldKey); err == nil {
		r.Close()
		t.Fatal("old S3 object not removed")
	}
	for _, key := range []string{key, v.StorageKey} {
		r, err := store.GetObject(ctx, key)
		if err != nil {
			t.Fatal("retained S3 object unavailable", err)
		}
		r.Close()
	}
	t.Log("real PostgreSQL and MinIO: old full archive removed; latest full and historical artifact bytes retained")
}
