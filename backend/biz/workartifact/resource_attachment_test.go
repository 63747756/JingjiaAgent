package workartifact

import (
	"context"
	"strings"
	"testing"

	"github.com/63747756/jingjiaagent/backend/pkg/asseturl"
	"github.com/google/uuid"
)

func TestResourceAttachmentChecksTaskOwnerProjectAndExactVersion(t *testing.T) {
	s, owner, reader, project, task := testStorage(t)
	ctx := context.Background()
	store := s.store.(*memoryStore)
	key := "tmp/" + owner.String() + "_source.csv"
	store.objects[key] = []byte("amount\n1\n")
	private, err := s.RegisterResource(ctx, owner, RegisterResourceRequest{URL: asseturl.Build(key), Filename: "source.csv"})
	if err != nil {
		t.Fatal(err)
	}
	first := private.CurrentVersionID
	store.objects[key] = []byte("amount\n99\n")
	updated, err := s.AddResourceVersion(ctx, owner, private.ID, RegisterResourceRequest{URL: asseturl.Build(key), Filename: "source.csv"})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := s.ResourceAttachment(ctx, owner, ResourceAttachmentRequest{VersionID: first, TaskID: task})
	if err != nil || attachment.Size != int64(len("amount\n1\n")) || !strings.HasPrefix(attachment.MIMEType, "text/csv") {
		t.Fatalf("old version could not be reused: %#v %v", attachment, err)
	}
	copyKey, ok := asseturl.Parse(attachment.URL)
	if !ok || !asseturl.AllowedForUser(copyKey, owner, s.cfg.ObjectStorage) {
		t.Fatal("history attachment did not use the normal owner-scoped asset route")
	}
	if string(store.objects[copyKey]) != "amount\n1\n" {
		t.Fatal("history reference drifted to a newer version")
	}
	if _, err = s.ResourceAttachment(ctx, reader, ResourceAttachmentRequest{VersionID: first, TaskID: task}); err == nil {
		t.Fatal("another employee resolved the owner's task")
	}
	readerTask := uuid.New()
	if _, err = s.db.ExecContext(ctx, `INSERT INTO tasks(id,user_id) VALUES($1,$2);`, readerTask, reader); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO project_tasks(task_id,project_id) VALUES($1,$2)`, readerTask, project); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResourceAttachment(ctx, reader, ResourceAttachmentRequest{VersionID: first, TaskID: readerTask}); err == nil {
		t.Fatal("personal uploads leaked to a project collaborator")
	}
	shared, err := s.RegisterResource(ctx, owner, RegisterResourceRequest{URL: asseturl.Build(key), Filename: "shared.csv", ProjectID: &project})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResourceAttachment(ctx, reader, ResourceAttachmentRequest{VersionID: shared.CurrentVersionID, TaskID: readerTask}); err != nil {
		t.Fatalf("project file could not be reused by a member: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE project_collaborators SET deleted_at=now() WHERE user_id=$1`, reader); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResourceAttachment(ctx, reader, ResourceAttachmentRequest{VersionID: shared.CurrentVersionID, TaskID: readerTask}); err == nil {
		t.Fatal("revoked member retained access")
	}
	for _, version := range updated.Versions {
		if version.ID == first {
			store.objects[version.StorageKey] = []byte("changed bytes")
		}
	}
	if _, err = s.ResourceAttachment(ctx, owner, ResourceAttachmentRequest{VersionID: first, TaskID: task}); err == nil {
		t.Fatal("corrupt historical bytes were handed to the runtime")
	}
}
