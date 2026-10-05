package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/samber/do"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/pkg/git/giturl"
	"github.com/63747756/jingjiaagent/backend/pkg/lifecycle"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

// GitTaskUsecase GitTask 业务逻辑实现
type GitTaskUsecase struct {
	cfg           *config.Config
	logger        *slog.Logger
	repo          domain.GitTaskRepoInterface
	taskflow      taskflow.Clienter
	redis         *redis.Client
	taskLifecycle *lifecycle.Manager[uuid.UUID, consts.TaskStatus, lifecycle.TaskMetadata]
	vmLifecycle   *lifecycle.Manager[string, lifecycle.VMState, lifecycle.VMMetadata]
}

// NewGitTaskUsecase 创建 GitTaskUsecase
func NewGitTaskUsecase(i *do.Injector) (domain.GitTaskUsecase, error) {
	return &GitTaskUsecase{
		cfg:           do.MustInvoke[*config.Config](i),
		logger:        do.MustInvoke[*slog.Logger](i).With("module", "usecase.GitTaskUsecase"),
		repo:          do.MustInvoke[domain.GitTaskRepoInterface](i),
		taskflow:      do.MustInvoke[taskflow.Clienter](i),
		redis:         do.MustInvoke[*redis.Client](i),
		taskLifecycle: do.MustInvoke[*lifecycle.Manager[uuid.UUID, consts.TaskStatus, lifecycle.TaskMetadata]](i),
		vmLifecycle:   do.MustInvoke[*lifecycle.Manager[string, lifecycle.VMState, lifecycle.VMMetadata]](i),
	}, nil
}

func (g *GitTaskUsecase) CheckAdmission(ctx context.Context) error {
	if gate, ok := g.taskflow.(taskflow.ReviewAdmissionGate); ok {
		return gate.CheckNewReview(ctx)
	}
	return nil
}

// Create implements domain.GitTaskUsecase.
func (g *GitTaskUsecase) Create(ctx context.Context, req domain.CreateGitTaskReq) (*domain.GitTask, error) {
	if strings.Contains(req.Body, "> 我是 [JingjiaAgent 编程助手]") {
		g.logger.With("comment", req.Subject.ID).Info("ignore comment from JingjiaAgent 编程助手")
		return nil, nil
	}
	if err := g.CheckAdmission(ctx); err != nil {
		return nil, err
	}

	// Do not mutate the caller's map: webhook retry payloads must stay stable.
	env := make(map[string]string, len(req.Env)+2)
	for key, value := range req.Env {
		env[key] = value
	}
	env["BASE_URL"] = g.cfg.Server.BaseURL

	stager, transactional := g.taskflow.(taskflow.TransactionalCreator)
	admissionRepo, supportsAdmission := g.repo.(domain.GitTaskAdmissionRepo)
	if transactional && !supportsAdmission {
		return nil, fmt.Errorf("git task repository does not support transactional runtime admission")
	}
	var createTaskReq *taskflow.CreateTaskReq
	persistLegacy := func() error {
		b, err := json.Marshal(createTaskReq)
		if err != nil {
			return err
		}
		reqKey := fmt.Sprintf("jingjiaagent:task:create_req:%s", createTaskReq.ID.String())
		if err := g.redis.Set(ctx, reqKey, string(b), createReqTTL(g.cfg)).Err(); err != nil {
			return fmt.Errorf("failed to store git task request")
		}
		return nil
	}

	create := func(u *db.User, t *db.Task, m *db.Model) (*taskflow.VirtualMachine, error) {
		branch := "master"
		if req.Repo.Branch != nil {
			branch = *req.Repo.Branch
		}

		vm, err := g.taskflow.VirtualMachiner().Create(ctx, &taskflow.CreateVirtualMachineReq{
			UserID:   u.ID.String(),
			HostID:   req.HostID,
			HostName: t.ID.String(),
			Git: taskflow.Git{
				// Codeup 仓库 URL 必须带 .git 后缀才能 clone，做一次兜底归一化
				URL:      giturl.NormalizeCloneURL(req.Repo.URL),
				Username: "JingjiaAgent",
				Email:    "jingjiaagent@chaitin.com",
				Branch:   branch,
				Token:    req.Git.Token,
			},
			ImageURL: g.cfg.ReviewAgent.Image,
			TaskID:   t.ID,
			LLM: taskflow.LLMProviderReq{
				Provider: taskflow.LlmProviderOpenAI,
				ApiKey:   m.APIKey,
				BaseURL:  m.BaseURL,
				Model:    m.Model,
			},
			Cores:    fmt.Sprintf("%d", g.cfg.Task.Core),
			Memory:   g.cfg.Task.Memory,
			LogStore: normalizeTaskLogStore(t.LogStore),
		})
		if err != nil {
			return nil, err
		}
		if vm == nil {
			return nil, fmt.Errorf("failed to create virtual machine")
		}

		// Lifecycle 状态转换
		taskMeta := lifecycle.TaskMetadata{TaskID: t.ID, UserID: u.ID}
		if err := g.taskLifecycle.Transition(ctx, t.ID, consts.TaskStatusPending, taskMeta); err != nil {
			g.logger.WarnContext(ctx, "task lifecycle transition failed", "error", err)
		}

		vmMeta := lifecycle.VMMetadata{VMID: vm.ID, TaskID: &t.ID, UserID: u.ID}
		if err := g.vmLifecycle.Transition(ctx, vm.ID, lifecycle.VMStatePending, vmMeta); err != nil {
			g.logger.WarnContext(ctx, "vm lifecycle transition failed", "error", err)
		}

		env["TASK_ID"] = t.ID.String()
		createTaskReq = &taskflow.CreateTaskReq{
			ID:          t.ID,
			VMID:        vm.ID,
			Text:        req.Prompt,
			CodingAgent: taskflow.CodingAgentMCAIReview,
			LLM: taskflow.LLM{
				ApiKey:  m.APIKey,
				BaseURL: m.BaseURL,
				Model:   m.Model,
				ApiType: m.InterfaceType,
			},
			Env:      env,
			LogStore: normalizeTaskLogStore(t.LogStore),
		}
		if !transactional {
			if err := persistLegacy(); err != nil {
				return vm, err
			}
		}

		return vm, nil
	}
	var tk *db.Task
	var err error
	if transactional {
		tk, err = admissionRepo.CreateWithAdmission(ctx, req, create, func(ctx context.Context, tx *db.Tx) error {
			if createTaskReq == nil {
				return fmt.Errorf("git task admission request is missing")
			}
			durable, err := stager.StageTaskInTx(ctx, tx, *createTaskReq)
			if err != nil {
				// Do not include upstream diagnostics that may contain credentials.
				return fmt.Errorf("failed to persist git task runtime admission")
			}
			if !durable {
				return persistLegacy()
			}
			return nil
		})
	} else {
		tk, err = g.repo.Create(ctx, req, create)
	}
	if err != nil {
		g.logger.With("error", err).ErrorContext(ctx, "failed to create git task")
		return nil, err
	}

	result := &domain.GitTask{
		ID:                   tk.ID,
		TaskID:               tk.ID,
		SubjectURL:           req.Subject.URL,
		PromptID:             req.PromptID,
		GithubInstallationID: req.GithubInstallationID,
		Platform:             req.Platform,
		Repo: &domain.GitTaskRepo{
			URL:      req.Repo.URL,
			Platform: req.Platform,
		},
	}

	g.logger.With("task_id", tk.ID).InfoContext(ctx, "git task created")
	return result, nil
}
