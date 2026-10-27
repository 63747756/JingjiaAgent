package v1

import (
	"io"
	"log/slog"
	"testing"

	"github.com/63747756/jingjiaagent/backend/biz/task/service"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/middleware"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/63747756/jingjiaagent/backend/pkg/tasklog"
	"github.com/63747756/jingjiaagent/backend/pkg/ws"
	"github.com/GoYoko/web"
	"github.com/samber/do"
)

type taskRouteUsecase struct{ domain.TaskUsecase }
type taskRouteUserUsecase struct{ domain.UserUsecase }
type taskRouteActiveRepo struct{ domain.UserActiveRepo }
type taskRouteTaskflow struct{ taskflow.Clienter }
type taskRouteActivity struct{ service.TaskActivityRefresher }

func TestTaskRoutesRetainHistoryAndControlWithoutDirectCreation(t *testing.T) {
	i := do.New()
	w := web.New()
	do.ProvideValue(i, w)
	do.ProvideValue(i, &config.Config{})
	do.ProvideValue(i, slog.New(slog.NewTextHandler(io.Discard, nil)))
	do.ProvideValue[domain.TaskUsecase](i, taskRouteUsecase{})
	do.ProvideValue[domain.UserUsecase](i, taskRouteUserUsecase{})
	do.ProvideValue[domain.UserActiveRepo](i, taskRouteActiveRepo{})
	do.ProvideValue[taskflow.Clienter](i, taskRouteTaskflow{})
	do.ProvideValue[service.TaskActivityRefresher](i, taskRouteActivity{})
	do.ProvideValue(i, &tasklog.Gateway{})
	do.ProvideValue(i, &middleware.AuthMiddleware{})
	do.ProvideValue(i, &middleware.TargetActiveMiddleware{})
	do.ProvideValue(i, ws.NewTaskConn())
	do.ProvideValue(i, ws.NewControlConn())
	do.ProvideValue(i, &service.TaskSummaryService{})
	if _, err := NewTaskHandler(i); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"GET /api/v1/users/tasks":                       false,
		"GET /api/v1/users/tasks/:id":                   false,
		"GET /api/v1/users/tasks/stream":                false,
		"GET /api/v1/users/tasks/public-stream":         false,
		"GET /api/v1/users/tasks/control":               false,
		"GET /api/v1/users/tasks/rounds":                false,
		"GET /api/v1/users/tasks/user-inputs":           false,
		"PUT /api/v1/users/tasks/stop":                  false,
		"DELETE /api/v1/users/tasks/:id":                false,
		"PUT /api/v1/users/tasks/:id":                   false,
		"PUT /api/v1/users/tasks/:id/model":             false,
		"POST /api/v1/users/tasks/speech-to-text":       false,
		"GET /api/v1/users/tasks/speech-to-text-stream": false,
	}
	for _, route := range w.Routes() {
		key := route.Method + " " + route.Path
		if key == "POST /api/v1/users/tasks" {
			t.Fatal("legacy direct task creation route is still registered")
		}
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for route, registered := range want {
		if !registered {
			t.Errorf("existing history or control route removed: %s", route)
		}
	}
}
