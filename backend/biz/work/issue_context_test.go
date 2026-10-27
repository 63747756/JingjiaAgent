package work

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/middleware"
	"github.com/GoYoko/web"
	"github.com/google/uuid"
	"github.com/samber/do"
)

type issueContextProjects struct {
	domain.ProjectRepo
	owner       uuid.UUID
	project     uuid.UUID
	issue       *db.ProjectIssue
	readOnly    bool
	lookupCount int
}

func (p *issueContextProjects) HasReadWritePerm(_ context.Context, user, project uuid.UUID) bool {
	return !p.readOnly && user == p.owner && project == p.project
}

func (p *issueContextProjects) GetIssue(_ context.Context, user, project, issue uuid.UUID) (*db.ProjectIssue, error) {
	p.lookupCount++
	if user != p.owner || project != p.issue.ProjectID || issue != p.issue.ID {
		return nil, errcode.ErrNotFound
	}
	return p.issue, nil
}

func TestWorkIssueContextRequiresProjectWriteAndLoadsExactRequirement(t *testing.T) {
	for _, scenario := range []string{"allowed", "read-only", "other project", "missing issue", "missing project id", "missing issue id"} {
		t.Run(scenario, func(t *testing.T) {
			s, _ := mockedService(t)
			user, project, issue := uuid.New(), uuid.New(), uuid.New()
			repo := &issueContextProjects{owner: user, project: project, issue: &db.ProjectIssue{ID: issue, ProjectID: project, Title: "需求标题", RequirementDocument: "需求内容", DesignDocument: "设计方案"}}
			s.projects = repo
			req := IssueContextRequest{ProjectID: project, IssueID: issue}
			want := error(nil)
			switch scenario {
			case "read-only":
				repo.readOnly = true
				want = errcode.ErrForbidden
			case "other project":
				repo.issue.ProjectID = uuid.New()
				want = errcode.ErrNotFound
			case "missing issue":
				req.IssueID = uuid.New()
				want = errcode.ErrNotFound
			case "missing project id":
				req.ProjectID = uuid.Nil
				want = errcode.ErrBadRequest
			case "missing issue id":
				req.IssueID = uuid.Nil
				want = errcode.ErrBadRequest
			}
			out, err := s.IssueContext(context.Background(), &domain.User{ID: user}, req)
			if !errors.Is(err, want) {
				t.Fatalf("got %v want %v", err, want)
			}
			if want == nil && (out.ID != issue || out.Title != repo.issue.Title || out.RequirementDocument != repo.issue.RequirementDocument || out.DesignDocument != repo.issue.DesignDocument) {
				t.Fatalf("requirement context lost: %+v", out)
			}
			if (want == errcode.ErrForbidden || want == errcode.ErrBadRequest) && repo.lookupCount != 0 {
				t.Fatal("invalid request or permission still read requirement contents")
			}
		})
	}
}

func TestWorkRegistersIssueContextAndUnifiedCreation(t *testing.T) {
	s, _ := mockedService(t)
	i := do.New()
	w := web.New()
	do.ProvideValue(i, w)
	do.ProvideValue(i, s)
	do.ProvideValue(i, middleware.NewAuthMiddleware(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))))
	do.ProvideValue(i, &middleware.TargetActiveMiddleware{})
	if _, err := NewHandler(i); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"GET /api/v1/users/work/issue-context": false, "POST /api/v1/users/work-tasks": false}
	for _, route := range w.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for route, found := range want {
		if !found {
			t.Errorf("missing route: %s", route)
		}
	}
}
