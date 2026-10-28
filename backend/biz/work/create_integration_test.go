package work

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/63747756/jingjiaagent/backend/biz/agentresource"
	"github.com/63747756/jingjiaagent/backend/biz/workartifact"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/enttest"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

type createModels struct {
	domain.ModelRepo
	model   *db.Model
	allowed map[uuid.UUID]*db.Model
}

func (m createModels) Get(_ context.Context, _ uuid.UUID, id uuid.UUID) (*db.Model, error) {
	if m.allowed != nil {
		if model := m.allowed[id]; model != nil {
			return model, nil
		}
		return nil, errors.New("model not authorized")
	}
	return m.model, nil
}

type createHosts struct{ domain.HostRepo }

func (createHosts) GetHost(context.Context, uuid.UUID, string) (*domain.Host, error) {
	return &domain.Host{}, nil
}

type createImages struct{ domain.ImageRepo }

func (createImages) GetByID(context.Context, uuid.UUID, uuid.UUID) (*db.Image, error) {
	return &db.Image{}, nil
}

type captureTasks struct {
	domain.TaskUsecase
	req domain.CreateTaskReq
}

func (c *captureTasks) Create(_ context.Context, _ *domain.User, req domain.CreateTaskReq) (*domain.ProjectTask, error) {
	c.req = req
	id := uuid.New()
	return &domain.ProjectTask{ID: id, Task: &domain.Task{ID: id, Type: consts.TaskTypeWork}}, nil
}

type captureInputs struct {
	versions []uuid.UUID
	resource uuid.UUID
	fail     bool
}

func (c *captureInputs) ResolveInputs(_ context.Context, _ uuid.UUID, _ *uuid.UUID, versions []uuid.UUID) ([]domain.TaskAttachment, []workartifact.InputSnapshot, error) {
	c.versions = versions
	if c.fail {
		return nil, nil, errors.New("input revoked")
	}
	return []domain.TaskAttachment{{URL: "https://objects.internal/fixed-version", Filename: "原始表格.xlsx"}}, []workartifact.InputSnapshot{{ResourceID: c.resource, VersionID: versions[0], Filename: "原始表格.xlsx", SHA256: "fixed-hash"}}, nil
}
func (c *captureInputs) RegisterResource(context.Context, uuid.UUID, workartifact.RegisterResourceRequest) (workartifact.Resource, error) {
	return workartifact.Resource{}, errors.New("unexpected upload")
}

func TestWorkCreateUsesServerProfileAndExactInputVersion(t *testing.T) {
	for _, scenario := range []string{"fixed input", "revoked input", "repository branch", "long ASCII input", "long Chinese input"} {
		t.Run(scenario, func(t *testing.T) {
			revoke := scenario == "revoked input"
			s, m := mockedService(t)
			ctx := context.Background()
			uid, team, pid, model, image, version := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			c := enttest.Open(t, "sqlite3", "file:work-create-"+uuid.NewString()+"?mode=memory&cache=shared&_fk=1")
			t.Cleanup(func() { c.Close() })
			s.ent = c
			s.skillRepo = agentresource.NewRepo(c)
			s.objects = &memorySkills{map[string][]byte{}}
			s.cfg.Runtime.Backend = "agent_compose"
			s.models = createModels{model: &db.Model{ID: model, BaseURL: "http://model.internal:8000/v1"}}
			s.hosts = createHosts{}
			s.images = createImages{}
			tasks := &captureTasks{}
			inputs := &captureInputs{resource: uuid.New(), fail: revoke}
			s.tasks = tasks
			s.inputs = inputs
			m.ExpectQuery("SELECT .*FROM work_execution_profiles").WithArgs(uid, false).WillReturnRows(sqlmock.NewRows([]string{"id", "team", "name", "description", "model", "host", "image", "cli", "skills", "core", "memory", "enabled", "default", "instructions", "task_presets", "mentionable_skills"}).AddRow(pid, team, "内网工作", "", model, "server-node", image, "opencode", `[]`, 2, int64(8<<30), true, true, "校验数据来源", `[{"id":"monthly-review","name":"月度复盘","description":"按管理员提供的口径复盘","icon":"chart-column","enabled":true}]`, `[]`))
			var req CreateRequest
			body, _ := json.Marshal(map[string]any{"content": "分析此表", "task_preset_id": "monthly-review", "team_id": team, "resource_version_ids": []uuid.UUID{version}, "host_id": "employee-injected-node", "cli_name": "employee-injected-cli"})
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "long ASCII input":
				req.Content = strings.Repeat("full task specification\n", 24000) + "END"
			case "long Chinese input":
				req.Content = strings.Repeat("请完整分析需求并实现功能。\n", 12000) + "结束"
			}

			if scenario == "repository branch" {
				project := &db.Project{ID: uuid.New(), RepoURL: "https://git.example/repo.git", Rules: "keep project conventions"}
				s.projects = createProjects{project: project}
				req.ProjectID = &project.ID
				req.Branch = "feature/reports"
			}
			out, err := s.Create(ctx, &domain.User{ID: uid}, req)
			if len(inputs.versions) != 1 || inputs.versions[0] != version {
				t.Fatal("input resource resolved with an unfixed identifier")
			}
			if revoke {
				if err == nil || tasks.req.Content != "" {
					t.Fatalf("revoked input admitted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tasks.req.Content != req.Content {
				t.Fatalf("task content was changed or truncated: got %d bytes, want %d", len(tasks.req.Content), len(req.Content))
			}
			if tasks.req.ModelID != model.String() || tasks.req.HostID != "server-node" || tasks.req.CliName != consts.CliNameOpencode || tasks.req.Type != consts.TaskTypeWork || !tasks.req.PinnedSkillMode || tasks.req.WorkAdmission == nil {
				t.Fatalf("server execution profile bypassed: %#v", tasks.req)
			}
			if !strings.Contains(tasks.req.SystemPrompt, "校验数据来源") || !strings.Contains(tasks.req.SystemPrompt, "按管理员提供的口径复盘") || out.Work.TaskPreset == nil || out.Work.TaskPreset.Icon != "chart-column" {
				t.Fatal("server assistant instructions or task preset not applied")
			}
			if tasks.req.ProjectID != req.ProjectID || tasks.req.RepoReq.Branch != req.Branch || out.Work.InputResources[0].VersionID != version {
				t.Fatalf("independent work gained code/project or mutable input: %#v", out)
			}
		})
	}
}

func TestWorkCreateRequestRetainsOtherValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*CreateRequest)
	}{
		{"empty content", func(req *CreateRequest) { req.Content = " \n\t" }},
		{"output requirements", func(req *CreateRequest) { req.OutputRequirements = strings.Repeat("x", 8193) }},
		{"legacy skill count", func(req *CreateRequest) { req.SkillIDs = make([]string, 33) }},
		{"skill selection count", func(req *CreateRequest) { req.SkillSelection = &SkillSelection{IDs: make([]string, 129)} }},
		{"attachment count", func(req *CreateRequest) { req.Attachments = make([]domain.TaskAttachment, 11) }},
		{"combined input count", func(req *CreateRequest) {
			req.ResourceVersionIDs = []uuid.UUID{uuid.New()}
			req.Attachments = make([]domain.TaskAttachment, 10)
		}},
		{"invalid preset", func(req *CreateRequest) { req.TaskPresetID = "invalid preset" }},
		{"zero resource version", func(req *CreateRequest) { req.ResourceVersionIDs = []uuid.UUID{uuid.Nil} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := CreateRequest{Content: strings.Repeat("正文", 40000)}
			tc.mutate(&req)
			if err := validateRequest(&req); !errors.Is(err, errcode.ErrBadRequest) {
				t.Fatalf("invalid request was accepted: %v", err)
			}
		})
	}
}

type createProjects struct {
	domain.ProjectRepo
	project  *db.Project
	readOnly bool
}

func (p createProjects) Get(context.Context, uuid.UUID, uuid.UUID) (*db.Project, error) {
	return p.project, nil
}
func (p createProjects) HasReadWritePerm(context.Context, uuid.UUID, uuid.UUID) bool {
	return !p.readOnly
}
