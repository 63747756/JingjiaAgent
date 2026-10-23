package work

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/enttest"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

type optionModels struct {
	createModels
	items []*db.Model
}

func (m optionModels) List(context.Context, uuid.UUID, domain.CursorReq) ([]*db.Model, *db.Cursor, error) {
	return m.items, nil, nil
}

func expectOptionProfile(m sqlmock.Sqlmock, uid, pid, team, model uuid.UUID, cli string) {
	m.ExpectQuery("SELECT .*FROM work_execution_profiles").WithArgs(uid, false).WillReturnRows(sqlmock.NewRows([]string{"id", "team", "name", "description", "model", "host", "image", "cli", "skills", "core", "memory", "enabled", "default", "instructions", "task_presets", "mentionable_skills"}).AddRow(pid, team, "assistant", "", model, "server-node", uuid.New(), cli, `[]`, 2, int64(8<<30), true, true, "rules", `[]`, `[]`))
}

func TestModelOptionsAllowPublicAndPrivateProvidersWithoutCredentials(t *testing.T) {
	s, mock := mockedService(t)
	uid, pid, team := uuid.New(), uuid.New(), uuid.New()
	base := &db.Model{ID: uuid.New(), Model: "default", InterfaceType: "openai_responses", BaseURL: "http://model.internal:8000/v1", APIKey: "must-not-leak"}
	chat := &db.Model{ID: uuid.New(), Model: "chat", InterfaceType: "openai_chat", BaseURL: base.BaseURL}
	remote := &db.Model{ID: uuid.New(), Model: "remote", InterfaceType: "openai_responses", BaseURL: "https://outside.example/v1"}
	s.models = optionModels{createModels: createModels{model: base}, items: []*db.Model{base, chat, remote}}
	s.hosts, s.images = createHosts{}, createImages{}
	expectOptionProfile(mock, uid, pid, team, base.ID, "codex")
	out, err := s.ModelOptions(context.Background(), &domain.User{ID: uid}, ModelOptionsRequest{ProfileID: &pid})
	if err != nil || len(out.Items) != 2 || out.Items[0].ID != base.ID || out.DefaultModelID != base.ID {
		t.Fatalf("options=%+v error=%v", out, err)
	}
	b, _ := json.Marshal(out)
	for _, forbidden := range []string{"api_key", "base_url", "must-not-leak", "model.internal"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("credential detail leaked: %s", forbidden)
		}
	}
}

func TestCreateModelSelectionChecksGrantsProtocolAndHostBeforeInputs(t *testing.T) {
	for _, mode := range []string{"default", "override", "unauthorized", "protocol", "invalid URL"} {
		t.Run(mode, func(t *testing.T) {
			s, mock := mockedService(t)
			uid, pid, team, mid, selected, version := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			base := &db.Model{ID: mid, Model: "default", InterfaceType: "openai_responses", BaseURL: "http://model.internal:8000/v1"}
			next := &db.Model{ID: selected, Model: "next", InterfaceType: "openai_responses", BaseURL: base.BaseURL}
			allowed := map[uuid.UUID]*db.Model{mid: base, selected: next}
			if mode == "unauthorized" {
				delete(allowed, selected)
			}
			if mode == "protocol" {
				next.InterfaceType = "anthropic"
			}
			if mode == "invalid URL" {
				next.BaseURL = "file:///etc/passwd"
			}
			s.models = createModels{allowed: allowed}
			s.hosts, s.images = createHosts{}, createImages{}
			s.cfg.Runtime.Backend = "agent_compose"
			expectOptionProfile(mock, uid, pid, team, mid, "codex")
			tasks, inputs := &captureTasks{}, &captureInputs{resource: uuid.New()}
			s.tasks, s.inputs = tasks, inputs
			s.ent = enttest.Open(t, "sqlite3", "file:model-choice-"+uuid.NewString()+"?mode=memory&cache=shared&_fk=1")
			t.Cleanup(func() { s.ent.Close() })
			req := CreateRequest{Content: "work", ProfileID: &pid, ResourceVersionIDs: []uuid.UUID{version}}
			if mode != "default" {
				req.ModelID = &selected
			}
			_, err := s.Create(context.Background(), &domain.User{ID: uid}, req)
			if mode != "default" && mode != "override" {
				if err == nil || inputs.versions != nil || tasks.req.Content != "" {
					t.Fatalf("invalid model admitted: %v", err)
				}
				return
			}
			want := mid
			if mode == "override" {
				want = selected
			}
			if err != nil || tasks.req.ModelID != want.String() || tasks.req.CliName != consts.CliNameCodex || tasks.req.HostID != "server-node" {
				t.Fatalf("selection not honored: %v %+v", err, tasks.req)
			}
		})
	}
}
