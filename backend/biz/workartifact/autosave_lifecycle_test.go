package workartifact

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/63747756/jingjiaagent/backend/pkg/workspacesnapshot"
	"github.com/google/uuid"
)

type autosaveRuntimeStub struct {
	checkpointRuntime
	service              *Service
	data                 []byte
	exports, completions int
	resumeError          bool
}

func (r *autosaveRuntimeStub) ExportCheckpoint(context.Context, uuid.UUID, uuid.UUID, string) (io.ReadCloser, error) {
	r.exports++
	return io.NopCloser(bytes.NewReader(r.data)), nil
}

func (r *autosaveRuntimeStub) ExportArtifacts(ctx context.Context, _ uuid.UUID, id uuid.UUID, _ string) (io.ReadCloser, error) {
	r.exports++
	z, err := zip.NewReader(bytes.NewReader(r.data), int64(len(r.data)))
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "direct-artifacts-test-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	for _, entry := range z.File {
		if !workspacesnapshot.ArtifactPath(entry.Name) {
			continue
		}
		p := filepath.Join(dir, ".jingjiaagent", filepath.FromSlash(entry.Name))
		if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			return nil, err
		}
		in, err := entry.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(in)
		in.Close()
		if err != nil {
			return nil, err
		}
		if err = os.WriteFile(p, data, 0600); err != nil {
			return nil, err
		}
	}
	var b bytes.Buffer
	if err = workspacesnapshot.ExportArtifacts(ctx, dir, id.String(), &b); err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(b.Bytes())), nil
}

func (r *autosaveRuntimeStub) CompleteCheckpoint(ctx context.Context, tid, cid uuid.UUID, env string) error {
	r.completions++
	var committed bool
	if err := r.service.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_checkpoints WHERE id=$1 AND state='committed') OR EXISTS(SELECT 1 FROM work_storage_jobs WHERE id=$1 AND request->>'artifacts_saved'='true')`, cid).Scan(&committed); err != nil {
		return err
	}
	if !committed {
		return errors.New("resume called before durable checkpoint commit")
	}
	if r.resumeError {
		return errors.New("resume unavailable")
	}
	_, err := r.service.db.ExecContext(ctx, "UPDATE runtime_environments SET state='online',archiving_checkpoint_id=NULL WHERE id=$1", env)
	return err
}

func TestAutosaveCommitsFilesAndRetriesResumeWithoutReexport(t *testing.T) {
	for _, failedUpload := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume-failure", true: "storage-failure"}[failedUpload], func(t *testing.T) {
			s, owner, _, _, task := testStorage(t)
			ctx := context.Background()
			if _, err := s.db.Exec("ALTER TABLE runtime_environments ADD COLUMN owner_id uuid,ADD COLUMN state text; CREATE TABLE runtime_task_intents(task_id uuid PRIMARY KEY,environment_id text)"); err != nil {
				t.Fatal(err)
			}
			env := "autosave-environment"
			id := uuid.New()
			if _, err := s.db.Exec("INSERT INTO runtime_environments(id,owner_id,state,archiving_checkpoint_id) VALUES($1,$2,'checkpointing',$3)", env, owner, id); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("INSERT INTO runtime_task_intents VALUES($1,$2)", task, env); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("INSERT INTO work_storage_jobs(id,task_id,environment_id,request) VALUES($1,$2,$3,'{\"resume_state\":\"online\"}')", id, task, env); err != nil {
				t.Fatal(err)
			}
			archive, _ := archiveFixture(t, id, map[string][]byte{"output/report.txt": []byte("confirmed report")}, nil)
			// archiveFixture leaves its file at EOF.
			info, err := archive.Stat()
			if err != nil {
				t.Fatal(err)
			}
			data := make([]byte, info.Size())
			if _, err = archive.ReadAt(data, 0); err != nil {
				t.Fatal(err)
			}
			runtime := &autosaveRuntimeStub{service: s, data: data, resumeError: !failedUpload}
			s.runtime = runtime
			s.store.(*memoryStore).corrupt = failedUpload
			if err := s.Step(ctx); err == nil {
				t.Fatal("failed preservation or resume reported success")
			}
			var state string
			if err := s.db.QueryRow("SELECT state FROM runtime_environments WHERE id=$1", env).Scan(&state); err != nil || state != "checkpointing" {
				t.Fatalf("failure lost the preservation fence: %s %v", state, err)
			}
			if failedUpload && runtime.completions != 0 {
				t.Fatal("failed storage resumed before a durable snapshot existed")
			}
			runtime.resumeError = false
			s.store.(*memoryStore).corrupt = false
			if _, err := s.db.Exec("UPDATE work_storage_jobs SET available_at=now() WHERE id=$1", id); err != nil {
				t.Fatal(err)
			}
			if err := s.Step(ctx); err != nil {
				t.Fatal(err)
			}
			var complete bool
			if err := s.db.QueryRow("SELECT j.state='complete' AND j.kind='artifacts' AND e.state='online' AND NOT EXISTS(SELECT 1 FROM workspace_checkpoints WHERE task_id=j.task_id) FROM work_storage_jobs j JOIN runtime_environments e ON e.id=j.environment_id WHERE j.id=$1", id).Scan(&complete); err != nil || !complete {
				t.Fatalf("autosave left the environment asleep: %v %v", complete, err)
			}
			versions, err := s.Versions(ctx, owner, uuid.NewSHA1(task, []byte("output/report.txt")))
			if err != nil || len(versions) != 1 {
				t.Fatalf("retry duplicated the saved artifact: %d %v", len(versions), err)
			}
			if !failedUpload && runtime.exports != 1 {
				t.Fatalf("resume retry exported the already committed workspace again: %d", runtime.exports)
			}
		})
	}
}
