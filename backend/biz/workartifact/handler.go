package workartifact

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/middleware"
	"github.com/GoYoko/web"
	"github.com/google/uuid"
	"github.com/samber/do"
)

type Handler struct{ s *Service }

func ProvideWorkArtifact(i *do.Injector) { do.Provide(i, NewService); do.Provide(i, NewHandler) }
func InvokeWorkArtifact(i *do.Injector)  { do.MustInvoke[*Handler](i) }
func NewHandler(i *do.Injector) (*Handler, error) {
	h := &Handler{s: do.MustInvoke[*Service](i)}
	w := do.MustInvoke[*web.Web](i)
	auth := do.MustInvoke[*middleware.AuthMiddleware](i)
	target := do.MustInvoke[*middleware.TargetActiveMiddleware](i)
	g := w.Group("/api/v1/users")
	g.Use(auth.Auth(), target.TargetActive())
	g.GET("/work/resources", web.BaseHandler(h.resources))
	g.POST("/work/resources", web.BaseHandler(h.registerResource))
	g.POST("/work/resources/attachment", web.BaseHandler(h.resourceAttachment))
	g.GET("/work/resources/:id", web.BaseHandler(h.resource))
	g.POST("/work/resources/:id/versions", web.BaseHandler(h.addResourceVersion))
	g.GET("/projects/:id/resources", web.BaseHandler(h.projectResources))
	g.GET("/work/artifacts", web.BaseHandler(h.artifacts))
	g.GET("/tasks/:id/artifacts", web.BaseHandler(h.taskArtifacts))
	g.POST("/tasks/:id/artifacts", web.BaseHandler(h.registerArtifacts))
	g.GET("/artifacts/:id", web.BaseHandler(h.artifact))
	g.GET("/artifacts/:id/versions", web.BaseHandler(h.versions))
	g.GET("/artifacts/:id/download", web.BaseHandler(h.download))
	g.GET("/artifacts/:id/preview", web.BaseHandler(h.preview))
	g.POST("/artifacts/:id/publish", web.BaseHandler(h.publish))
	g.DELETE("/artifacts/:id", web.BaseHandler(h.deleteArtifact))
	g.GET("/tasks/:id/checkpoints", web.BaseHandler(h.checkpoints))
	g.GET("/tasks/:id/storage-status", web.BaseHandler(h.storageStatus))
	g.POST("/tasks/:id/storage-retry", web.BaseHandler(h.retryStorage))
	g.POST("/tasks/:id/checkpoints", web.BaseHandler(h.checkpoint))
	g.POST("/tasks/:id/restore", web.BaseHandler(h.restore))
	g.GET("/tasks/:id/continuations", web.BaseHandler(h.continuations))
	g.POST("/tasks/:id/continuations/retry", web.BaseHandler(h.retryContinuation))
	return h, nil
}
func param(c *web.Context) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return uuid.Nil, errcode.ErrBadRequest
	}
	return id, nil
}
func queryID(c *web.Context, name string) (*uuid.UUID, error) {
	if c.QueryParam(name) == "" {
		return nil, nil
	}
	id, err := uuid.Parse(c.QueryParam(name))
	if err != nil {
		return nil, errcode.ErrBadRequest
	}
	return &id, nil
}
func body(c *web.Context, v any) error {
	return json.NewDecoder(io.LimitReader(c.Request().Body, 1<<20)).Decode(v)
}
func (h *Handler) resources(c *web.Context) error {
	pid, err := queryID(c, "project_id")
	if err != nil {
		return err
	}
	items, err := h.s.ListResources(c.Request().Context(), middleware.GetUser(c).ID, pid)
	if err != nil {
		return err
	}
	return c.Success(map[string]any{"items": items})
}

func (h *Handler) resourceAttachment(c *web.Context) error {
	var req ResourceAttachmentRequest
	if err := body(c, &req); err != nil {
		return errcode.ErrBadRequest
	}
	out, err := h.s.ResourceAttachment(c.Request().Context(), middleware.GetUser(c).ID, req)
	if err != nil {
		return err
	}
	return c.Success(out)
}
func (h *Handler) projectResources(c *web.Context) error {
	pid, err := param(c)
	if err != nil {
		return err
	}
	items, err := h.s.ListResources(c.Request().Context(), middleware.GetUser(c).ID, &pid)
	if err != nil {
		return err
	}
	return c.Success(map[string]any{"items": items})
}
func (h *Handler) registerResource(c *web.Context) error {
	var r RegisterResourceRequest
	if err := body(c, &r); err != nil {
		return errcode.ErrBadRequest
	}
	out, err := h.s.RegisterResource(c.Request().Context(), middleware.GetUser(c).ID, r)
	if err != nil {
		return err
	}
	return c.Success(out)
}
func (h *Handler) addResourceVersion(c *web.Context) error {
	id, err := param(c)
	if err != nil {
		return err
	}
	var r RegisterResourceRequest
	if err = body(c, &r); err != nil {
		return errcode.ErrBadRequest
	}
	out, err := h.s.AddResourceVersion(c.Request().Context(), middleware.GetUser(c).ID, id, r)
	if err != nil {
		return err
	}
	return c.Success(out)
}
func (h *Handler) resource(c *web.Context) error {
	id, err := param(c)
	if err != nil {
		return err
	}
	out, err := h.s.Resource(c.Request().Context(), middleware.GetUser(c).ID, id)
	if err != nil {
		return err
	}
	return c.Success(out)
}
func (h *Handler) artifacts(c *web.Context) error {
	pid, err := queryID(c, "project_id")
	if err != nil {
		return err
	}
	tid, err := queryID(c, "task_id")
	if err != nil {
		return err
	}
	page, err := artifactPageQuery(c)
	if err != nil {
		return err
	}
	out, err := h.s.ListArtifacts(c.Request().Context(), middleware.GetUser(c).ID, pid, tid, page)
	if err != nil {
		return err
	}
	return c.Success(out)
}
func (h *Handler) taskArtifacts(c *web.Context) error {
	id, err := param(c)
	if err != nil {
		return err
	}
	page, err := artifactPageQuery(c)
	if err != nil {
		return err
	}
	out, err := h.s.ListArtifacts(c.Request().Context(), middleware.GetUser(c).ID, nil, &id, page)
	if err != nil {
		return err
	}
	return c.Success(out)
}

func artifactPageQuery(c *web.Context) (ArtifactPageRequest, error) {
	req := ArtifactPageRequest{Limit: 24, Cursor: c.QueryParam("cursor")}
	if value := c.QueryParam("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 100 {
			return req, errcode.ErrBadRequest
		}
		req.Limit = limit
	}
	return req, nil
}
func (h *Handler) registerArtifacts(c *web.Context) error {
	id, err := param(c)
	if err != nil {
		return err
	}
	var r struct {
		Paths []string `json:"paths"`
	}
	if err = body(c, &r); err != nil {
		return errcode.ErrBadRequest
	}
	job, err := h.s.queue(c.Request().Context(), middleware.GetUser(c).ID, id, r.Paths)
	if err != nil {
		return err
	}
	return c.Success(map[string]any{"job_id": job, "status": "pending"})
}
func (h *Handler) artifact(c *web.Context) error {
	id, err := param(c)
	if err != nil {
		return err
	}
	out, err := h.s.Artifact(c.Request().Context(), middleware.GetUser(c).ID, id)
	if err != nil {
		return err
	}
	return c.Success(out)
}
func (h *Handler) versions(c *web.Context) error {
	id, err := param(c)
	if err != nil {
		return err
	}
	out, err := h.s.Versions(c.Request().Context(), middleware.GetUser(c).ID, id)
	if err != nil {
		return err
	}
	return c.Success(map[string]any{"items": out})
}
func (h *Handler) sendFile(c *web.Context, preview bool) error {
	id, err := param(c)
	if err != nil {
		return err
	}
	vid, err := queryID(c, "version_id")
	if err != nil {
		return err
	}
	v := uuid.Nil
	if vid != nil {
		v = *vid
	}
	r, name, mt, err := h.s.Download(c.Request().Context(), middleware.GetUser(c).ID, id, v, preview)
	if err != nil {
		return err
	}
	defer r.Close()
	disposition := "attachment"
	if preview {
		disposition = "inline"
		c.Response().Header().Set("Content-Security-Policy", previewContentPolicy(mt))
		c.Response().Header().Set("Referrer-Policy", "no-referrer")
		c.Response().Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	}
	c.Response().Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	c.Response().Header().Set("Content-Type", mt)
	c.Response().Header().Set("Cache-Control", "private, no-store")
	c.Response().Header().Set("X-Content-Type-Options", "nosniff")
	c.Response().WriteHeader(http.StatusOK)
	_, err = io.Copy(c.Response().Writer, r)
	return err
}
func (h *Handler) download(c *web.Context) error { return h.sendFile(c, false) }
func (h *Handler) preview(c *web.Context) error  { return h.sendFile(c, true) }
func (h *Handler) publish(c *web.Context) error {
	id, err := param(c)
	if err != nil {
		return err
	}
	var r struct {
		ProjectID uuid.UUID `json:"project_id"`
		VersionID uuid.UUID `json:"version_id"`
	}
	if err = body(c, &r); err != nil || r.ProjectID == uuid.Nil {
		return errcode.ErrBadRequest
	}
	if err = h.s.Publish(c.Request().Context(), middleware.GetUser(c).ID, id, r.ProjectID, r.VersionID); err != nil {
		return err
	}
	return c.Success(nil)
}
func (h *Handler) deleteArtifact(c *web.Context) error {
	id, err := param(c)
	if err != nil {
		return err
	}
	if err = h.s.DeleteArtifact(c.Request().Context(), middleware.GetUser(c).ID, id); err != nil {
		return err
	}
	return c.Success(nil)
}
func (h *Handler) checkpoints(c *web.Context) error {
	id, err := param(c)
	if err != nil {
		return err
	}
	out, err := h.s.Checkpoints(c.Request().Context(), middleware.GetUser(c).ID, id)
	if err != nil {
		return err
	}
	return c.Success(map[string]any{"items": out})
}
func (h *Handler) checkpoint(c *web.Context) error {
	id, err := param(c)
	if err != nil {
		return err
	}
	job, err := h.s.queue(c.Request().Context(), middleware.GetUser(c).ID, id, nil)
	if err != nil {
		return err
	}
	return c.Success(map[string]any{"job_id": job, "status": "pending"})
}
func (h *Handler) restore(c *web.Context) error {
	id, err := param(c)
	if err != nil {
		return err
	}
	var r struct {
		CheckpointID uuid.UUID `json:"checkpoint_id"`
	}
	if c.Request().ContentLength != 0 {
		if err = body(c, &r); err != nil {
			return errcode.ErrBadRequest
		}
	}
	env, err := h.s.Restore(c.Request().Context(), middleware.GetUser(c).ID, id, r.CheckpointID)
	if err != nil {
		return err
	}
	return c.Success(map[string]any{"environment_id": env, "status": "preparing"})
}
