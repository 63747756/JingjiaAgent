package workartifact

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/63747756/jingjiaagent/backend/pkg/workspacesnapshot"
	"github.com/google/uuid"
)

func TestFullWorkspaceSaveFailureIsVisibleBoundedAndRetryable(t *testing.T) {
	s, owner, other, _, task := testStorage(t)
	ctx := context.Background()
	env := "full-workspace"
	id := uuid.New()
	if _, err := s.db.Exec(`ALTER TABLE runtime_environments ADD COLUMN owner_id uuid,ADD COLUMN state text; CREATE TABLE runtime_task_intents(task_id uuid PRIMARY KEY,environment_id text);`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO runtime_environments(id,owner_id,state,archiving_checkpoint_id) VALUES($1,$2,'checkpointing',$3);`, env, owner, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO runtime_task_intents VALUES($1,$2)`, task, env); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO work_storage_jobs(id,task_id,environment_id,request) VALUES($1,$2,$3,'{"scope":"workspace","resume_state":"hibernated"}')`, id, task, env); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for name, data := range map[string]string{"code/main.go": "package main", ".git/HEAD": "ref: refs/heads/main\n", ".jingjiaagent/output/report.txt": "saved report", ".jingjiaagent/attachments/task/round/input.txt": "original attachment"} {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0700)
		os.WriteFile(p, []byte(data), 0600)
	}
	var archive bytes.Buffer
	if err := workspacesnapshot.Export(ctx, root, id.String(), &archive); err != nil {
		t.Fatal(err)
	}
	runtime := &autosaveRuntimeStub{service: s, data: archive.Bytes()}
	s.runtime = runtime
	// A full backup has its own limits. Artifact limits/conversion must not
	// prevent preserving source files or output files inside the workspace.
	s.cfg.Work.MaxFileBytes = 4
	s.store.(*memoryStore).corrupt = true
	for i := 0; i < 3; i++ {
		if _, err := s.db.Exec(`UPDATE work_storage_jobs SET available_at=now() WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
		if err := s.Step(ctx); err == nil {
			t.Fatal("corrupt storage permitted save")
		}
	}
	if _, err := s.db.Exec(`UPDATE work_storage_jobs SET available_at=now() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("retry did not stop after three attempts", err)
	}
	if runtime.exports != 3 || runtime.completions != 0 {
		t.Fatal("failed save completed/retried indefinitely")
	}
	items, err := s.StorageStatus(ctx, owner, task)
	if err != nil || len(items) != 1 || !items[0].Paused || items[0].Error == "" {
		t.Fatal("save failure not visible", items, err)
	}
	if err := s.RetryStorage(ctx, other, task, id); err == nil {
		t.Fatal("other employee retried private workspace")
	}
	if err := s.RetryStorage(ctx, owner, task, id); err != nil {
		t.Fatal(err)
	}
	s.store.(*memoryStore).corrupt = false
	if err := s.Step(ctx); err != nil {
		t.Fatal(err)
	}
	var durable bool
	if err := s.db.QueryRow(`SELECT state='committed' AND manifest->>'schema'=$2 FROM workspace_checkpoints WHERE id=$1`, id, workspacesnapshot.Schema).Scan(&durable); err != nil || !durable {
		t.Fatal("full checkpoint not committed", err)
	}
	var artifacts int
	if err := s.db.QueryRow(`SELECT count(*) FROM work_artifacts WHERE task_id=$1`, task).Scan(&artifacts); err != nil || artifacts != 0 {
		t.Fatal("full backup changed independently managed artifact versions", artifacts, err)
	}
	items, err = s.StorageStatus(ctx, owner, task)
	if err != nil || len(items) != 0 {
		t.Fatal("resolved failure remains visible", items, err)
	}
}

func TestFullSnapshotCanonicalOverlayKeepsRepositoryAndAttachments(t *testing.T) {
	s, _, _, _, task := testStorage(t)
	ctx := context.Background()
	id := uuid.New()
	root := t.TempDir()
	for name, data := range map[string]string{"main.py": "local code", ".jingjiaagent/attachments/a.txt": "input", ".jingjiaagent/output/report.txt": "result"} {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0700)
		os.WriteFile(p, []byte(data), 0600)
	}
	var b bytes.Buffer
	if err := workspacesnapshot.Export(ctx, root, id.String(), &b); err != nil {
		t.Fatal(err)
	}
	f, _, size, err := spool(bytes.NewReader(b.Bytes()), workspacesnapshot.MaxArchiveBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer removeSpool(f)
	m, z, err := verifyArchive(f, size, id, 100<<20, 256<<20, 2000)
	if err != nil {
		t.Fatal(err)
	}
	out, _, _, err := s.restoreArchive(ctx, task, m, z)
	if err != nil || out != nil {
		t.Fatal("full snapshot was downgraded to products-only archive", err)
	}
}

func TestFullRestoreOverlayRetainsAllNonOutputFiles(t *testing.T) {
	s, owner, _, _, task := testStorage(t)
	ctx := context.Background()
	raw := saveConversionFixture(t, s, owner, task, "output/report.xlsx", "original spreadsheet")
	canonical := saveConversionFixture(t, s, owner, task, "output/report.xlsx", "recalculated spreadsheet")
	if _, err := s.db.Exec(`UPDATE work_artifact_versions SET source_version_id=$2,validation_status='passed' WHERE id=$1`, canonical.ID, raw.ID); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	root := t.TempDir()
	files := map[string]string{"main.py": "local code", ".git/HEAD": "ref: refs/heads/main", ".jingjiaagent/attachments/a.txt": "input", ".jingjiaagent/output/report.xlsx": "original spreadsheet"}
	for name, data := range files {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0700)
		os.WriteFile(p, []byte(data), 0600)
	}
	var b bytes.Buffer
	if err := workspacesnapshot.Export(ctx, root, id.String(), &b); err != nil {
		t.Fatal(err)
	}
	f, _, size, err := spool(bytes.NewReader(b.Bytes()), workspacesnapshot.MaxArchiveBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer removeSpool(f)
	m, z, err := verifyArchive(f, size, id, 100<<20, 256<<20, 2000)
	if err != nil {
		t.Fatal(err)
	}
	out, _, size, err := s.restoreArchive(ctx, task, m, z)
	if err != nil || out == nil {
		t.Fatal("full archive canonical overlay failed", err)
	}
	defer removeSpool(out)
	m, z, err = verifyArchive(out, size, id, 100<<20, 256<<20, 2000)
	if err != nil || m.Schema != workspacesnapshot.Schema {
		t.Fatal("full archive metadata lost", err)
	}
	destination := t.TempDir()
	if err := workspacesnapshot.Restore(ctx, z, id.String(), destination); err != nil {
		t.Fatal(err)
	}
	files[".jingjiaagent/output/report.xlsx"] = "recalculated spreadsheet"
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(destination, name))
		if err != nil || string(got) != want {
			t.Fatal("file changed by canonical overlay", name, err)
		}
	}
}

func TestCheckpointsChooseLastSuccessfulCaptureNotOldJobCreation(t *testing.T) {
	s, owner, _, _, task := testStorage(t)
	ctx := context.Background()
	olderJob, newerJob := uuid.New(), uuid.New()
	if _, err := s.db.Exec(`INSERT INTO runtime_environments(id) VALUES('env')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO workspace_checkpoints(id,task_id,environment_id,state,created_at,committed_at) VALUES($1,$3,'env','committed',now()-interval '1 hour',now()),($2,$3,'env','committed',now()-interval '30 minutes',now()-interval '10 minutes')`, olderJob, newerJob, task); err != nil {
		t.Fatal(err)
	}
	items, err := s.Checkpoints(ctx, owner, task)
	if err != nil || len(items) != 2 || items[0].ID != olderJob {
		t.Fatal("latest capture was hidden behind newer queued job", items, err)
	}
}

func TestFullRecoveryDetectsQueuedRoundExecutedAfterCapture(t *testing.T) {
	s, owner, _, _, task := testStorage(t)
	ctx := context.Background()
	id := uuid.New()
	if _, err := s.db.Exec(`CREATE TABLE runtime_commands(id uuid PRIMARY KEY,task_id uuid,turn integer,operation text,submission_started boolean,created_at timestamptz);INSERT INTO runtime_environments(id) VALUES('env');`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO workspace_checkpoints(id,task_id,environment_id,state,manifest,created_at,committed_at) VALUES($1,$2,'env','committed','{"schema":"jingjiaagent.workspace.checkpoint.v3"}',now()-interval '20 minutes',now()-interval '5 minutes')`, id, task); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO work_storage_jobs(id,task_id,environment_id,state,request) VALUES($1,$2,'env','complete','{"scope":"workspace","captured_turn":1}')`, id, task); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO runtime_commands VALUES($1,$2,2,'task',true,now()-interval '10 minutes')`, uuid.New(), task); err != nil {
		t.Fatal(err)
	}
	_, err := s.restoreContinuation(ctx, owner, task, uuid.Nil)
	if err == nil || !strings.Contains(err.Error(), "最新工作未完整保存") {
		t.Fatal("queued round's newer unsaved work was silently lost", err)
	}
}
