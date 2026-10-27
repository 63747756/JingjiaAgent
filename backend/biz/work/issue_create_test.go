package work

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/63747756/jingjiaagent/backend/biz/agentresource"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/enttest"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestWorkCreateIssueRequestValidation(t *testing.T) {
	project, issue := uuid.New(), uuid.New()
	for _, tc := range []struct {
		name  string
		body  string
		valid bool
	}{
		{"independent work", `{"content":"完成任务"}`, true},
		{"project work without requirement", `{"content":"完成任务","project_id":"` + project.String() + `"}`, true},
		{"requirement work", `{"content":"设计需求方案","project_id":"` + project.String() + `","issue_id":"` + issue.String() + `"}`, true},
		{"requirement without project", `{"content":"完成任务","issue_id":"` + issue.String() + `"}`, false},
		{"zero requirement", `{"content":"完成任务","project_id":"` + project.String() + `","issue_id":"` + uuid.Nil.String() + `"}`, false},
		{"malformed requirement", `{"content":"完成任务","project_id":"` + project.String() + `","issue_id":"invalid"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req CreateRequest
			err := json.Unmarshal([]byte(tc.body), &req)
			if err == nil {
				err = validateRequest(&req)
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func issueCreateFixture(t *testing.T) (*Service, sqlmock.Sqlmock, *domain.User, CreateRequest, *captureTasks) {
	t.Helper()
	ctx := context.Background()
	s, mock := mockedService(t)
	s.cfg.Runtime.Backend = "agent_compose"
	s.ent = enttest.Open(t, "sqlite3", "file:work-issue-create-"+uuid.NewString()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { s.ent.Close() })
	uid, team, profile, model, version := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	s.ent.User.Create().SetID(uid).SetName("owner").SetRole(consts.UserRoleEnterprise).SetStatus(consts.UserStatusActive).SaveX(ctx)
	project := s.ent.Project.Create().SetID(uuid.New()).SetUserID(uid).SetName("项目").SetRepoURL("https://git.example/repo.git").SetDefaultProfileID(profile).SaveX(ctx)
	issue := s.ent.ProjectIssue.Create().SetID(uuid.New()).SetUserID(uid).SetProjectID(project.ID).SetTitle("需求").SaveX(ctx)
	s.projects = createProjects{project: project}
	s.models = createModels{model: &db.Model{ID: model, BaseURL: "http://model.internal:8000/v1"}}
	s.hosts = createHosts{}
	s.images = createImages{}
	s.inputs = &captureInputs{resource: uuid.New()}
	s.skillRepo = agentresource.NewRepo(s.ent)
	s.objects = &memorySkills{map[string][]byte{}}
	tasks := &captureTasks{}
	s.tasks = tasks
	return s, mock, &domain.User{ID: uid}, CreateRequest{Content: "设计并实现需求", ProjectID: &project.ID, IssueID: &issue.ID, TeamID: &team, ResourceVersionIDs: []uuid.UUID{version}, Branch: "feature/requirement"}, tasks
}

func expectIssueCreateProfile(mock sqlmock.Sqlmock, s *Service, user *domain.User, req CreateRequest) {
	project := s.projects.(createProjects).project
	model := s.models.(createModels).model.ID
	mock.ExpectQuery("SELECT .*FROM work_execution_profiles").WithArgs(user.ID, false).WillReturnRows(sqlmock.NewRows([]string{"id", "team", "name", "description", "model", "host", "image", "cli", "skills", "core", "memory", "enabled", "default", "instructions", "task_presets", "mentionable_skills"}).AddRow(*project.DefaultProfileID, *req.TeamID, "项目默认助手", "", model, "server-node", uuid.New(), "opencode", `[]`, 2, int64(8<<30), true, true, "遵循项目开发约定", `[]`, `[]`))
}

func TestWorkCreateRequirementAssociationAndProjectPermission(t *testing.T) {
	for _, scenario := range []string{"without requirement", "associated", "read-only project", "other project", "missing requirement", "deleted requirement"} {
		t.Run(scenario, func(t *testing.T) {
			s, mock, user, req, tasks := issueCreateFixture(t)
			ctx := context.Background()
			want := error(nil)
			switch scenario {
			case "without requirement":
				req.IssueID = nil
			case "read-only project":
				project := s.projects.(createProjects)
				project.readOnly = true
				s.projects = project
				want = errcode.ErrForbidden
			case "other project":
				other := s.ent.Project.Create().SetID(uuid.New()).SetUserID(user.ID).SetName("其他项目").SaveX(ctx)
				s.ent.ProjectIssue.UpdateOneID(*req.IssueID).SetProjectID(other.ID).SaveX(ctx)
				want = errcode.ErrBadRequest
			case "missing requirement":
				id := uuid.New()
				req.IssueID = &id
				want = errcode.ErrBadRequest
			case "deleted requirement":
				s.ent.ProjectIssue.UpdateOneID(*req.IssueID).SetDeletedAt(time.Now()).SaveX(ctx)
				want = errcode.ErrBadRequest
			}
			if want == nil {
				expectIssueCreateProfile(mock, s, user, req)
			}
			out, err := s.Create(ctx, user, req)
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			if want != nil {
				if tasks.req.Content != "" || len(s.inputs.(*captureInputs).versions) != 0 {
					t.Fatal("invalid requirement or project permission reached task creation or input resolution")
				}
				return
			}
			wantIssue := uuid.Nil
			if req.IssueID != nil {
				wantIssue = *req.IssueID
			}
			if tasks.req.Extra.IssueID != wantIssue || tasks.req.ProjectID == nil || *tasks.req.ProjectID != *req.ProjectID || tasks.req.Type != consts.TaskTypeWork {
				t.Fatalf("requirement was not passed to canonical work task creation: %+v", tasks.req)
			}
			project := s.projects.(createProjects).project
			if out.Work.ProfileID != *project.DefaultProfileID || tasks.req.RepoReq.Branch != req.Branch || tasks.req.Content != req.Content || tasks.req.WorkAdmission == nil {
				t.Fatal("requirement work did not retain project default assistant, branch, content, and admission")
			}
		})
	}
}

func TestWorkCreateRechecksRequirementInsideAdmission(t *testing.T) {
	for _, scenario := range []string{"associated", "deleted after validation", "moved after validation", "write permission revoked"} {
		t.Run(scenario, func(t *testing.T) {
			s, mock, user, req, tasks := issueCreateFixture(t)
			expectIssueCreateProfile(mock, s, user, req)
			ctx := context.Background()
			if _, err := s.Create(ctx, user, req); err != nil {
				t.Fatal(err)
			}
			conn, admission, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			client := db.NewClient(db.Driver(entsql.OpenDB(dialect.Postgres, conn)))
			t.Cleanup(func() { client.Close() })
			admission.ExpectBegin()
			tx, err := client.Tx(ctx)
			if err != nil {
				t.Fatal(err)
			}
			admission.ExpectQuery(`SELECT .*team_members.*LIMIT 1`).WithArgs(user.ID, *req.TeamID).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uuid.New()))
			want := error(nil)
			if scenario == "write permission revoked" {
				project := s.projects.(createProjects)
				project.readOnly = true
				s.projects = project
				want = errcode.ErrForbidden
			} else {
				rows := sqlmock.NewRows([]string{"id"})
				if scenario == "associated" {
					rows.AddRow(*req.IssueID)
				} else {
					want = errcode.ErrBadRequest
				}
				admission.ExpectQuery(`SELECT .*project_issues.*project_id.*deleted_at.*FOR UPDATE`).WithArgs(*req.IssueID, *req.ProjectID).WillReturnRows(rows)
			}
			taskID := uuid.New()
			if want == nil {
				admission.ExpectExec("INSERT INTO work_tasks").WillReturnResult(sqlmock.NewResult(0, 1))
			}
			if err := tasks.req.WorkAdmission(ctx, tx, taskID); !errors.Is(err, want) {
				t.Fatalf("admission error=%v, want %v", err, want)
			}
			admission.ExpectRollback()
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := admission.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type issueReplayTasks struct {
	domain.TaskUsecase
	task *domain.Task
}

func (t issueReplayTasks) Info(context.Context, *domain.User, uuid.UUID) (*domain.Task, bool, error) {
	return t.task, false, nil
}

func TestWorkCreateIdempotencyIncludesRequirementAssociation(t *testing.T) {
	uid, requestID, taskID, team, profile, project, issue := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	original := CreateRequest{Content: "设计需求", ProjectID: &project, IssueID: &issue, ClientRequestID: &requestID}
	payload, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(payload)
	for _, scenario := range []string{"replay", "different requirement", "removed requirement"} {
		t.Run(scenario, func(t *testing.T) {
			s, mock := mockedService(t)
			s.cfg.Runtime.Backend = "agent_compose"
			s.store.lockDB = s.store.db
			req := original
			if scenario == "different requirement" {
				other := uuid.New()
				req.IssueID = &other
			} else if scenario == "removed requirement" {
				req.IssueID = nil
			}
			key := uid.String() + ":" + requestID.String()
			mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_lock(hashtextextended($1,0))`)).WithArgs(key).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery("SELECT task_id,request_payload_hash").WithArgs(uid, requestID).WillReturnRows(sqlmock.NewRows([]string{"task_id", "request_payload_hash"}).AddRow(taskID, hex.EncodeToString(hash[:])))
			if scenario == "replay" {
				mock.ExpectQuery("SELECT w.owner_id,w.team_id").WithArgs(taskID).WillReturnRows(sqlmock.NewRows([]string{"owner_id", "team_id", "project_id", "deleted"}).AddRow(uid, team, project.String(), false))
				mock.ExpectQuery("SELECT EXISTS.*team_members").WithArgs(uid, team).WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(true))
				mock.ExpectQuery("SELECT EXISTS.*projects").WithArgs(project.String(), uid, false).WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(true))
				mock.ExpectQuery("SELECT NOT EXISTS.*work_tasks.*resource_manifest").WithArgs(taskID, uid).WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(true))
				mock.ExpectQuery("SELECT profile_id,team_id").WithArgs(taskID).WillReturnRows(sqlmock.NewRows([]string{"profile_id", "team_id", "skill_manifest", "resource_manifest", "output_requirements", "delivery_status", "created_at", "assistant_name", "task_preset"}).AddRow(profile, team, `[]`, `[]`, "", "pending", time.Now(), "默认助手", nil))
				s.tasks = issueReplayTasks{task: &domain.Task{ID: taskID, ProjectID: &project, Type: consts.TaskTypeWork, Extra: &domain.TaskExtraConfig{IssueID: issue}}}
			}
			mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_unlock(hashtextextended($1,0))`)).WithArgs(key).WillReturnResult(sqlmock.NewResult(0, 1))
			out, err := s.Create(context.Background(), &domain.User{ID: uid}, req)
			if scenario != "replay" {
				if !errors.Is(err, errcode.ErrBadRequest) {
					t.Fatalf("changed issue association accepted: %v", err)
				}
				return
			}
			if err != nil || out.ID != taskID || out.Extra == nil || out.Extra.IssueID != issue {
				t.Fatalf("replayed task lost requirement association: %+v, %v", out, err)
			}
		})
	}
}
