package workartifact

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/google/uuid"
)

func TestDirectArtifactRoundsRetainHistoryWithoutBackupObjects(t *testing.T) {
	s, owner, _, _, task := testStorage(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`ALTER TABLE runtime_environments ADD COLUMN owner_id uuid,ADD COLUMN state text; CREATE TABLE runtime_task_intents(task_id uuid PRIMARY KEY,environment_id text);`); err != nil {
		t.Fatal(err)
	}
	env := "direct-artifacts"
	if _, err := s.db.Exec(`INSERT INTO runtime_environments(id,owner_id,state) VALUES($1,$2,'online')`, env, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO runtime_task_intents VALUES($1,$2)`, task, env); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"first report", "second report", "second report"} {
		id, command := uuid.New(), uuid.New()
		if _, err := s.db.Exec(`INSERT INTO work_storage_jobs(id,task_id,environment_id,command_id,kind,request) VALUES($1,$2,$3,$4,'artifacts','{"scope":"artifacts","resume_state":"online"}');`, id, task, env, command); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`UPDATE runtime_environments SET state='checkpointing',archiving_checkpoint_id=$2 WHERE id=$1`, env, id); err != nil {
			t.Fatal(err)
		}
		f, n := archiveFixture(t, id, map[string][]byte{"output/report.txt": []byte(text), "work/private.txt": []byte("not a result")}, nil)
		data := make([]byte, n)
		if _, err := f.ReadAt(data, 0); err != nil {
			t.Fatal(err)
		}
		s.runtime = &autosaveRuntimeStub{service: s, data: data}
		if err := s.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var checkpoints int
	if err := s.db.QueryRow(`SELECT count(*) FROM workspace_checkpoints WHERE task_id=$1`, task).Scan(&checkpoints); err != nil || checkpoints != 0 {
		t.Fatal("rounds created partial checkpoints", checkpoints, err)
	}
	versions, err := s.Versions(ctx, owner, uuid.NewSHA1(task, []byte("output/report.txt")))
	if err != nil || len(versions) != 2 {
		t.Fatal("history lost or unchanged report duplicated", len(versions), err)
	}
	for _, v := range versions {
		r, _, _, err := s.Download(ctx, owner, v.ArtifactID, v.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(r)
		r.Close()
		if !bytes.Contains(data, []byte("report")) {
			t.Fatal("historical content unavailable")
		}
	}
	for key := range s.store.(*memoryStore).objects {
		if strings.HasPrefix(key, "work-private/checkpoints/") {
			t.Fatal("partial backup stored", key)
		}
	}
}

type restoreDownloadStore struct {
	*memoryStore
	key              string
	started, release chan struct{}
}

func (s *restoreDownloadStore) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	if key == s.key {
		select {
		case <-s.started:
		default:
			close(s.started)
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.memoryStore.GetObject(ctx, key)
}

type pinnedRestoreRuntime struct {
	checkpointRuntime
	s   *Service
	env string
}

func (r *pinnedRestoreRuntime) RestoreCheckpoint(ctx context.Context, _ uuid.UUID, task, id uuid.UUID, _, key, digest string, _ []domain.TaskAttachment) (string, error) {
	_, err := r.s.db.ExecContext(ctx, `INSERT INTO runtime_restores(task_id,old_environment_id,new_environment_id,checkpoint_id,archive_key,payload_sha256,state) VALUES($1,$2,$2,$3,$4,$5,'preparing')`, task, r.env, id, key, digest)
	return r.env, err
}
func TestActualRestoreDownloadAndAsyncRegistrationProtectArchive(t *testing.T) {
	s, owner, _, _, task := testStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	old, key := retainedFixture(t, s, task, "env", true, time.Now().Add(-time.Minute))
	retainedFixture(t, s, task, "env", true, time.Now())
	store := &restoreDownloadStore{memoryStore: s.store.(*memoryStore), key: key, started: make(chan struct{}), release: make(chan struct{})}
	s.store = store
	s.runtime = &pinnedRestoreRuntime{s: s, env: "env"}
	done := make(chan error, 1)
	go func() { _, err := s.Restore(ctx, owner, task, old); done <- err }()
	select {
	case <-store.started:
	case <-ctx.Done():
		t.Fatal("restore did not start download")
	}
	if err := s.PruneCheckpoints(ctx, task); err != nil {
		t.Fatal(err)
	}
	assertCheckpointState(t, s, old, "committed")
	close(store.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.PruneCheckpoints(ctx, task); err != nil {
		t.Fatal(err)
	}
	assertCheckpointState(t, s, old, "committed")
	if _, err := s.db.Exec(`UPDATE runtime_restores SET state='complete' WHERE task_id=$1`, task); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneCheckpoints(ctx, task); err != nil {
		t.Fatal(err)
	}
	assertCheckpointState(t, s, old, "pruned")
}
