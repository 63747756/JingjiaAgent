package biz

import (
	"context"

	"github.com/google/uuid"
	"github.com/samber/do"

	"github.com/63747756/jingjiaagent/backend/biz/agentresource"
	"github.com/63747756/jingjiaagent/backend/biz/file"
	"github.com/63747756/jingjiaagent/backend/biz/git"
	"github.com/63747756/jingjiaagent/backend/biz/host"
	"github.com/63747756/jingjiaagent/backend/biz/llmproxy"
	"github.com/63747756/jingjiaagent/backend/biz/mcphub"
	"github.com/63747756/jingjiaagent/backend/biz/notify"
	"github.com/63747756/jingjiaagent/backend/biz/plugin"
	"github.com/63747756/jingjiaagent/backend/biz/project"
	"github.com/63747756/jingjiaagent/backend/biz/public"
	"github.com/63747756/jingjiaagent/backend/biz/server"
	"github.com/63747756/jingjiaagent/backend/biz/setting"
	"github.com/63747756/jingjiaagent/backend/biz/skill"
	"github.com/63747756/jingjiaagent/backend/biz/static"
	"github.com/63747756/jingjiaagent/backend/biz/subscription"
	"github.com/63747756/jingjiaagent/backend/biz/task"
	"github.com/63747756/jingjiaagent/backend/biz/team"
	teamrepo "github.com/63747756/jingjiaagent/backend/biz/team/repo"
	teamusecase "github.com/63747756/jingjiaagent/backend/biz/team/usecase"
	"github.com/63747756/jingjiaagent/backend/biz/uploader"
	"github.com/63747756/jingjiaagent/backend/biz/user"
	"github.com/63747756/jingjiaagent/backend/biz/vmidle"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/domain"
)

// RegisterAll 注册所有 biz 模块
// 分两阶段：先 Provide（懒注册），再 Invoke（解析依赖），避免模块间循环依赖
func RegisterAll(i *do.Injector) error {
	notify.ProvideNotify(i)
	public.ProvidePublic(i)
	user.ProvideUser(i)
	setting.ProvideSetting(i)
	team.ProvideTeam(i)
	host.ProvideHost(i)
	agentresource.ProvideAgentResource(i)
	task.ProvideTask(i)
	git.ProvideGit(i)
	project.ProvideProject(i)
	file.ProvideFile(i)
	vmidle.ProvideVMIdle(i)
	skill.ProvideSkill(i)
	plugin.ProvidePlugin(i)
	server.ProvideServer(i)
	return nil
}

func InvokeAll(i *do.Injector) {
	notify.InvokeNotify(i)
	public.InvokePublic(i)
	user.InvokeUser(i)
	setting.InvokeSetting(i)
	team.InvokeTeam(i)
	host.InvokeHost(i)
	task.InvokeTask(i)
	git.InvokeGit(i)
	project.InvokeProject(i)
	file.InvokeFile(i)
	vmidle.InvokeVMIdle(i)
	skill.InvokeSkill(i)
	plugin.InvokePlugin(i)
	server.InvokeServer(i)
}

// RegisterOpenSource 注册仅在开源项目中使用的模块
func RegisterOpenSource(i *do.Injector) {
	provideStandaloneMembers(i)
	host.ProvidePublicHost(i)
	subscription.ProvideSubscription(i)
	uploader.ProvideUploader(i)
	llmproxy.ProvideLLMProxy(i)
	mcphub.ProvideMCPHub(i)
	static.ProviderStatic(i)
	do.ProvideValue[domain.TaskHook](i, &taskhook{})
}

func provideStandaloneMembers(i *do.Injector) {
	if _, err := do.Invoke[domain.MemberManager](i); err != nil {
		do.Provide(i, teamrepo.NewLocalMemberStore)
		do.Provide(i, teamusecase.NewLocalMemberManager)
	}
}

func InvokeOpenSource(i *do.Injector) {
	subscription.InvokeSubscription(i)
	uploader.InvokeUploader(i)
	llmproxy.InvokeLLMProxy(i)
	mcphub.InvokeMCPHub(i)
	static.InvokeStatic(i)
}

type taskhook struct{}

// GetMaxConcurrent implements [domain.TaskHook].
func (t *taskhook) GetMaxConcurrent(ctx context.Context, uid uuid.UUID) (int, error) {
	return 3, nil
}

// GetSystemPrompt implements [domain.TaskHook].
func (t *taskhook) GetSystemPrompt(ctx context.Context, taskType consts.TaskType, subType consts.TaskSubType) (string, error) {
	return "", nil
}

// GitTask implements [domain.TaskHook].
func (t *taskhook) GitTask(ctx context.Context, id uuid.UUID) (*domain.GitTask, error) {
	return &domain.GitTask{}, nil
}

// OnTaskCreated implements [domain.TaskHook].
func (t *taskhook) OnTaskCreated(ctx context.Context, task *domain.ProjectTask) error {
	return nil
}

var _ domain.TaskHook = &taskhook{}
