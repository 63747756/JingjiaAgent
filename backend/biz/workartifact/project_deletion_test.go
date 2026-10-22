package workartifact

import (
	"context"
	"errors"
	"testing"

	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/asseturl"
	"github.com/63747756/jingjiaagent/backend/pkg/projectlifecycle"
	"github.com/google/uuid"
)

func TestProjectDeletionDoesNotRestoreRevokedUploaderAccess(t *testing.T) {
	for _, removal := range []string{"project", "team", "none"} {
		t.Run(removal, func(t *testing.T) {
			s, owner, uploader, project, task := testStorage(t)
			ctx := context.Background()
			// Use the real deletion transaction and resource access/attachment APIs.
			_, err := s.db.Exec(`ALTER TABLE projects ADD COLUMN updated_at timestamptz;
 ALTER TABLE tasks ADD COLUMN status text DEFAULT 'finished';
 CREATE TABLE runtime_task_intents(task_id uuid);
 CREATE TABLE runtime_commands(task_id uuid,state text);
 CREATE TABLE project_issues(id uuid,project_id uuid,deleted_at timestamptz);
 CREATE TABLE project_issue_comments(issue_id uuid,deleted_at timestamptz);
 CREATE TABLE project_git_bots(project_id uuid);
 CREATE TABLE navigation_pins(kind text,target_id uuid);`)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(`UPDATE project_collaborators SET role='read_write' WHERE project_id=$1 AND user_id=$2`, project, uploader); err != nil {
				t.Fatal(err)
			}
			store := s.store.(*memoryStore)
			key := "tmp/" + uploader.String() + "_original.txt"
			store.objects[key] = []byte("original upload")
			resource, err := s.RegisterResource(ctx, uploader, RegisterResourceRequest{URL: asseturl.Build(key), Filename: "report.txt", ProjectID: &project})
			if err != nil {
				t.Fatal(err)
			}
			first := resource.CurrentVersionID
			if _, err = s.db.Exec(`UPDATE work_tasks SET resource_manifest=jsonb_build_array(jsonb_build_object('resource_id',$2::text,'version_id',$3::text)) WHERE task_id=$1`, task, resource.ID, first); err != nil {
				t.Fatal(err)
			}
			switch removal {
			case "project":
				_, err = s.db.Exec(`UPDATE project_collaborators SET deleted_at=now() WHERE project_id=$1 AND user_id=$2`, project, uploader)
			case "team":
				_, err = s.db.Exec(`DELETE FROM team_members WHERE user_id=$1`, uploader)
			}
			if err != nil {
				t.Fatal(err)
			}
			key = "tmp/" + owner.String() + "_confidential.txt"
			store.objects[key] = []byte("confidential version added after revocation")
			resource, err = s.AddResourceVersion(ctx, owner, resource.ID, RegisterResourceRequest{URL: asseturl.Build(key), Filename: "report.txt"})
			if err != nil {
				t.Fatal(err)
			}
			if removal != "none" {
				if _, err = s.Resource(ctx, uploader, resource.ID); !errors.Is(err, errcode.ErrForbidden) {
					t.Fatalf("revoked uploader should already be denied: %v", err)
				}
			}
			if err = projectlifecycle.Delete(ctx, s.db, owner, project); err != nil {
				t.Fatal(err)
			}
			got, err := s.Resource(ctx, uploader, resource.ID)
			if removal == "none" {
				if err != nil || got.ProjectID != nil || len(got.Versions) != 2 {
					t.Fatalf("authorized uploader lost personal file: %+v %v", got, err)
				}
			} else {
				if !errors.Is(err, errcode.ErrForbidden) {
					t.Fatalf("revoked uploader regained access after deletion: %+v %v", got, err)
				}
				if _, _, err = s.ResolveInputs(ctx, uploader, nil, []uuid.UUID{resource.CurrentVersionID}); !errors.Is(err, errcode.ErrForbidden) {
					t.Fatalf("revoked uploader reused confidential input: %v", err)
				}
				list, err := s.ListResources(ctx, uploader, nil)
				if err != nil || len(list) != 0 {
					t.Fatalf("revoked project file became a personal file: %+v %v", list, err)
				}
			}
			// The remaining employee keeps only the version pinned in their task,
			// even when its original uploader no longer has project access.
			got, err = s.Resource(ctx, owner, resource.ID)
			if err != nil || len(got.Versions) != 1 || got.CurrentVersionID != first {
				t.Fatalf("retained task lost its exact input version: %+v %v", got, err)
			}
			if _, err = s.ResourceAttachment(ctx, owner, ResourceAttachmentRequest{TaskID: task, VersionID: first}); err != nil {
				t.Fatalf("retained task cannot reuse original input: %v", err)
			}
			if _, _, err = s.ResolveInputs(ctx, owner, nil, []uuid.UUID{resource.CurrentVersionID}); !errors.Is(err, errcode.ErrForbidden) {
				t.Fatalf("unreferenced newer version escaped retained grants: %v", err)
			}
		})
	}
}
