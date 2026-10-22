package workartifact

import (
	"bytes"
	"context"
	"testing"

	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
)

type revokedWorkGuard struct{}

func (revokedWorkGuard) CheckWorkTaskAccess(context.Context, uuid.UUID, uuid.UUID, bool) error {
	return errcode.ErrForbidden
}

func TestRevokedOwnerCannotReadOrPublishPrivateArtifacts(t *testing.T) {
	s, owner, reader, project, task := testStorage(t)
	ctx := context.Background()
	f, digest, n, err := spool(bytes.NewBufferString("# confirmed report\n"), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer removeSpool(f)
	if err = s.saveArtifact(ctx, owner, storageJob{TaskID: task}, "output/report.md", f, digest, n); err != nil {
		t.Fatal(err)
	}
	if err = s.convertTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	aid := uuid.NewSHA1(task, []byte("output/report.md"))
	a, err := s.Artifact(ctx, owner, aid)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Publish(ctx, owner, aid, project, a.CurrentVersionID); err != nil {
		t.Fatal(err)
	}
	s.guard = revokedWorkGuard{}
	if _, err = s.Artifact(ctx, owner, aid); err == nil {
		t.Fatal("revoked owner retained private artifact metadata")
	}
	if stream, _, _, e := s.Download(ctx, owner, aid, a.CurrentVersionID, false); e == nil {
		stream.Close()
		t.Fatal("revoked owner retained download access")
	}
	if err = s.Publish(ctx, owner, aid, project, a.CurrentVersionID); err == nil {
		t.Fatal("revoked owner retained publish access")
	}
	if err = s.DeleteArtifact(ctx, owner, aid); err == nil {
		t.Fatal("revoked owner retained artifact write access")
	}
	// An explicitly published version has its own current project grant. The
	// collaborator never receives the originating private task's permissions.
	if stream, _, _, e := s.Download(ctx, reader, aid, a.CurrentVersionID, false); e != nil {
		t.Fatalf("published version lost its independent project permission: %v", e)
	} else {
		stream.Close()
	}
}

func TestArchivePreservesRevokedOwnerWithoutGrantingUserAccess(t *testing.T) {
	s, owner, other, _, task := testStorage(t)
	ctx := context.Background()
	s.guard = revokedWorkGuard{}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE runtime_environments ADD COLUMN owner_id uuid; CREATE TABLE runtime_task_intents(task_id uuid PRIMARY KEY,environment_id text);`); err != nil {
		t.Fatal(err)
	}
	job := storageJob{ID: uuid.New(), TaskID: task, EnvironmentID: "original-environment"}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO runtime_environments(id,owner_id) VALUES($1,$2);`, job.EnvironmentID, owner.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO runtime_task_intents VALUES($1,$2)`, task, job.EnvironmentID); err != nil {
		t.Fatal(err)
	}
	assertSaved := func() {
		t.Helper()
		got, err := s.archiveOwner(ctx, job)
		if err != nil || got != owner {
			t.Fatalf("original owner's private preservation blocked: %v", err)
		}
	}
	assertDenied := func() {
		t.Helper()
		if _, err := s.archiveOwner(ctx, job); err == nil {
			t.Fatal("unrelated environment or inconsistent ownership accepted")
		}
	}
	assertSaved()
	if _, err := s.Checkpoints(ctx, owner, task); err == nil {
		t.Fatal("background preservation granted revoked user checkpoint access")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE runtime_environments SET owner_id=$1`, other.String()); err != nil {
		t.Fatal(err)
	}
	assertDenied()
	if _, err := s.db.ExecContext(ctx, `UPDATE runtime_environments SET owner_id=$1;`, owner.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET user_id=$1 WHERE id=$2`, other, task); err != nil {
		t.Fatal(err)
	}
	assertDenied()
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET user_id=$1 WHERE id=$2`, owner, task); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE runtime_task_intents SET environment_id='restored-environment'`); err != nil {
		t.Fatal(err)
	}
	assertDenied()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO workspace_checkpoints(id,task_id,environment_id,generation,state) VALUES($1,$2,$3,1,'committed')`, job.ID, task, job.EnvironmentID); err != nil {
		t.Fatal(err)
	}
	assertSaved() // Committed private files can still finish previews after restore.
	if _, err := s.Checkpoints(ctx, owner, task); err == nil {
		t.Fatal("committed checkpoint bypassed revoked user's current grants")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET deleted_at=now() WHERE id=$1`, task); err != nil {
		t.Fatal(err)
	}
	assertDenied()
}
