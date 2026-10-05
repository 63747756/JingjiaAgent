package v1

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/GoYoko/web"
	"github.com/63747756/jingjiaagent/backend/db/modelapikey"
	"github.com/63747756/jingjiaagent/backend/db/projecttask"
	"github.com/63747756/jingjiaagent/backend/db/task"
	"github.com/63747756/jingjiaagent/backend/db/virtualmachine"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

// The Guest uses its existing per-environment model credential. It never
// receives the broad Taskflow callback token. Scope is checked on every get;
// GitCredential then applies the existing current project/identity permission.
func (h *InternalHostHandler) RuntimeGitCredential(c *web.Context, req taskflow.GitCredentialRequest) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	parts := strings.Fields(c.Request().Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || h.runtimeGitScope(c.Request().Context(), parts[1], req) != nil {
		return c.String(http.StatusForbidden, "Git credential access denied")
	}
	return h.GitCredential(c, req)
}

func (h *InternalHostHandler) runtimeGitScope(ctx context.Context, token string, req taskflow.GitCredentialRequest) error {
	denied := errors.New("Git credential access denied")
	tid, err := uuid.Parse(req.TaskID)
	if err != nil || tid == uuid.Nil || h.runtimeDB == nil || token == "" || len(token) > 512 {
		return denied
	}
	key, err := h.runtimeDB.ModelApiKey.Query().Where(modelapikey.APIKey(token), modelapikey.KindEQ(modelapikey.KindRuntime)).Only(ctx)
	if err != nil || key.VirtualmachineID == "" {
		return denied
	}
	if req.VMID != "" && req.VMID != key.VirtualmachineID {
		return denied
	}
	bound, err := h.runtimeDB.Task.Query().Where(task.ID(tid), task.UserID(key.UserID),
		task.HasVmsWith(virtualmachine.ID(key.VirtualmachineID), virtualmachine.UserID(key.UserID),
			virtualmachine.Or(virtualmachine.IsRecycled(false), virtualmachine.IsRecycledIsNil()),
			virtualmachine.DeletedAtIsNil(), virtualmachine.EnvironmentIDNEQ(""))).Exist(ctx)
	if err != nil || !bound {
		return denied
	}
	pt, err := h.runtimeDB.ProjectTask.Query().Where(projecttask.TaskIDEQ(tid)).WithProject().Only(ctx)
	if err != nil {
		return denied
	}
	repo := pt.RepoURL
	if pt.Edges.Project != nil {
		repo = pt.Edges.Project.RepoURL
	}
	expected, err := url.Parse(repo)
	if err != nil || !gitCredentialTarget(expected, req) {
		return denied
	}
	return nil
}

func gitCredentialTarget(expected *url.URL, req taskflow.GitCredentialRequest) bool {
	if expected == nil || (expected.Scheme != "http" && expected.Scheme != "https") || expected.Host == "" || expected.User != nil || expected.RawQuery != "" || expected.Fragment != "" {
		return false
	}
	actual, err := url.Parse(req.Protocol + "://" + req.Host + "/" + strings.TrimPrefix(req.Path, "/"))
	if err != nil || actual.User != nil || actual.RawQuery != "" || actual.Fragment != "" || actual.Scheme != expected.Scheme || !strings.EqualFold(actual.Host, expected.Host) {
		return false
	}
	path := func(u *url.URL) string { return strings.TrimSuffix(strings.TrimSuffix(u.EscapedPath(), "/"), ".git") }
	return path(actual) == path(expected) && path(expected) != ""
}
