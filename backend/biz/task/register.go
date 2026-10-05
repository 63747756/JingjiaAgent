package task

import (
	"github.com/samber/do"

	v1 "github.com/63747756/jingjiaagent/backend/biz/task/handler/v1"
	"github.com/63747756/jingjiaagent/backend/biz/task/repo"
	"github.com/63747756/jingjiaagent/backend/biz/task/service"
	"github.com/63747756/jingjiaagent/backend/biz/task/usecase"
)

// ProvideTask 注册 task 模块的服务工厂
func ProvideTask(i *do.Injector) {
	do.Provide(i, usecase.NewTaskUsecase)
	do.Provide(i, usecase.NewGitTaskUsecase)
	do.Provide(i, service.NewTaskActivityRefresher)
	do.Provide(i, service.NewTaskSummaryService)
	do.Provide(i, v1.NewTaskHandler)
	do.Provide(i, repo.NewTaskRepo)
	do.Provide(i, repo.NewGitTaskRepo)
}

// InvokeTask 触发 task 模块的 handler 初始化
func InvokeTask(i *do.Injector) {
	do.MustInvoke[*v1.TaskHandler](i)
}
