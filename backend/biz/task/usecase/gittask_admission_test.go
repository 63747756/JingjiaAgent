package usecase

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/samber/do"

	taskrepo "github.com/chaitin/MonkeyCode/backend/biz/task/repo"
	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/consts"
	"github.com/chaitin/MonkeyCode/backend/db"
	"github.com/chaitin/MonkeyCode/backend/domain"
	"github.com/chaitin/MonkeyCode/backend/pkg/lifecycle"
	"github.com/chaitin/MonkeyCode/backend/pkg/runtimeadapter"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
)

type reviewAdmissionClient struct {
	*runtimeadapter.Client
	legacy bool
	before func(context.Context, taskflow.SQLExecutor, taskflow.CreateTaskReq)
	after  func(context.Context, taskflow.SQLExecutor, taskflow.CreateTaskReq) error
}

// The admission contract fixture deliberately bypasses the production gate:
// it proves persistence without claiming a working MCAIReview executor.
func (*reviewAdmissionClient) CheckNewReview(context.Context) error { return nil }

func (c *reviewAdmissionClient) StageTaskInTx(ctx context.Context, tx taskflow.SQLExecutor, req taskflow.CreateTaskReq) (bool, error) {
	if c.before != nil {
		c.before(ctx, tx, req)
	}
	durable, err := c.Client.StageTaskInTx(ctx, tx, req)
	if err == nil && c.after != nil {
		err = c.after(ctx, tx, req)
	}
	return durable, err
}

type reviewLegacyVM struct{ taskflow.VirtualMachiner }

func (*reviewLegacyVM) Create(_ context.Context, req *taskflow.CreateVirtualMachineReq) (*taskflow.VirtualMachine, error) {
	return &taskflow.VirtualMachine{ID: "legacy-" + req.TaskID.String(), EnvironmentID: "legacy-env", HostID: req.HostID}, nil
}

func (c *reviewAdmissionClient) VirtualMachiner() taskflow.VirtualMachiner {
	if c.legacy {
		return &reviewLegacyVM{}
	}
	return c.Client.VirtualMachiner()
}

type reviewFixture struct {
	u      *GitTaskUsecase
	db     *db.Client
	client *reviewAdmissionClient
	cfg    *config.Config
	redis  *miniredis.Miniredis
	logs   *bytes.Buffer
}

func newReviewFixture(t *testing.T) reviewFixture {
	t.Helper()
	dsn := os.Getenv("RUNTIME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL RUNTIME_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "review_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE"); _ = admin.Close() })
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	driver, err := entsql.Open(dialect.Postgres, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	client := db.NewClient(db.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })
	if err = client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	// Ent's test schema omits the UUID defaults supplied by business migrations.
	for _, table := range []string{"users", "git_tasks", "task_virtualmachines", "git_bot_tasks"} {
		if _, err = client.ExecContext(ctx, "ALTER TABLE "+table+" ALTER COLUMN id SET DEFAULT gen_random_uuid()"); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"000026_remote_runtime.up.sql", "000027_runtime_controls.up.sql", "000028_runtime_previews.up.sql", "000029_runtime_nodes.up.sql", "000030_runtime_capacity.up.sql", "000031_runtime_creation_attempts.up.sql"} {
		content, err := os.ReadFile(filepath.Join("..", "..", "..", "migration", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = client.ExecContext(ctx, string(content)); err != nil {
			t.Fatal(err)
		}
	}
	owner := uuid.New()
	client.User.Create().SetID(owner).SetName("fixture owner").SetRole(consts.UserRoleIndividual).SetStatus(consts.UserStatusActive).SaveX(ctx)
	client.Host.Create().SetID("node").SetUserID(owner).SaveX(ctx)
	model := client.Model.Create().SetID(uuid.New()).SetUserID(owner).SetProvider("OpenAI").SetAPIKey("review-fixture-model-secret").SetBaseURL("https://model.example.invalid/v1").SetModel("review-fixture").SetInterfaceType("openai_chat").SaveX(ctx)
	keyDir := t.TempDir()
	keyPath, tokenPath := filepath.Join(keyDir, "payload.key"), filepath.Join(keyDir, "node.token")
	if err = os.WriteFile(keyPath, bytes.Repeat([]byte{19}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(tokenPath, []byte("review-fixture-node-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Database.Master = parsed.String()
	cfg.Server.BaseURL = "http://127.0.0.1:1"
	cfg.TaskFlow.CallbackToken = "review-fixture-callback"
	cfg.ReviewAgent.ModelID, cfg.ReviewAgent.Image = model.ID.String(), "review-fixture-image"
	cfg.Task.Core, cfg.Task.Memory = 1, 2<<30
	cfg.Runtime = config.Runtime{Backend: "agent_compose", Experimental: true, PayloadKeyFile: keyPath, PollInterval: "100ms", Nodes: []config.RuntimeNode{{ID: "node", URL: "http://127.0.0.1:1", TokenFile: tokenPath, GuestImage: "review-fixture-image"}}}
	logs := new(bytes.Buffer)
	logger := slog.New(slog.NewTextHandler(logs, nil))
	adapter, err := runtimeadapter.NewClient(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	capture := &reviewAdmissionClient{Client: adapter}
	t.Cleanup(func() { _ = capture.Client.Close() })
	i := do.New()
	do.ProvideValue(i, cfg)
	do.ProvideValue(i, client)
	do.ProvideValue(i, logger)
	repo, err := taskrepo.NewGitTaskRepo(i)
	if err != nil {
		t.Fatal(err)
	}
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	u := &GitTaskUsecase{cfg: cfg, repo: repo, logger: logger, taskflow: capture, redis: rdb,
		taskLifecycle: lifecycle.NewManager[uuid.UUID, consts.TaskStatus, lifecycle.TaskMetadata](rdb, lifecycle.WithTransitions[uuid.UUID, consts.TaskStatus, lifecycle.TaskMetadata](lifecycle.TaskTransitions())),
		vmLifecycle:   lifecycle.NewManager[string, lifecycle.VMState, lifecycle.VMMetadata](rdb, lifecycle.WithTransitions[string, lifecycle.VMState, lifecycle.VMMetadata](lifecycle.VMTransitions()))}
	return reviewFixture{u: u, db: client, client: capture, cfg: cfg, redis: mr, logs: logs}
}

func reviewRequest() domain.CreateGitTaskReq {
	branch := "review-branch"
	return domain.CreateGitTaskReq{HostID: "node", Prompt: "https://git.example.invalid/pull/7", Platform: consts.GitPlatformGithub,
		User:    domain.User{Name: "review author", Email: "review@example.invalid"},
		Repo:    domain.Repo{URL: "https://git.example.invalid/review.git", Branch: &branch},
		Subject: domain.Subject{ID: "7", Number: 7, Type: "PullRequest", URL: "https://git.example.invalid/pull/7"},
		Git:     taskflow.Git{Token: "review-fixture-git-secret"}, Env: map[string]string{"GITHUB_TOKEN": "review-fixture-git-secret"}}
}

func reviewSQLCount(t *testing.T, q taskflow.SQLExecutor, table string) int {
	t.Helper()
	rows, err := q.QueryContext(context.Background(), "SELECT count(*) FROM "+table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var count int
	if !rows.Next() || rows.Scan(&count) != nil {
		t.Fatal("count query failed")
	}
	return count
}

func TestGitTaskTransactionalAdmissionAndRestart(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	f.client.after = func(ctx context.Context, tx taskflow.SQLExecutor, req taskflow.CreateTaskReq) error {
		for _, table := range []string{"tasks", "git_tasks", "virtualmachines", "task_virtualmachines", "runtime_task_intents", "runtime_commands"} {
			if reviewSQLCount(t, tx, table) != 1 || reviewSQLCount(t, f.db, table) != 0 {
				t.Fatalf("admission not isolated until product commit: %s", table)
			}
		}
		if err := f.client.Step(ctx); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("worker saw an uncommitted preparation: %v", err)
		}
		return nil
	}
	req := reviewRequest()
	created, err := f.u.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := req.Env["TASK_ID"]; exists {
		t.Fatal("caller webhook environment was mutated")
	}
	if f.redis.Exists("task:create_req:" + created.TaskID.String()) {
		t.Fatal("durable task still depends on an expiring Redis request")
	}
	f.client.Client.Close()
	f.client.Client, err = runtimeadapter.NewClient(f.cfg, f.u.logger)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := f.client.PreparedTask(ctx, created.TaskID.String())
	if err != nil || intent == nil || intent.CodingAgent != taskflow.CodingAgentMCAIReview || intent.LLM.ApiType != "openai_chat" || intent.Env["TASK_ID"] != created.TaskID.String() || intent.Env["BASE_URL"] != f.cfg.Server.BaseURL || intent.Env["GITHUB_TOKEN"] != req.Git.Token {
		t.Fatal("restart lost the dedicated review contract")
	}
	rows, err := f.db.QueryContext(ctx, "SELECT payload FROM runtime_task_intents")
	if err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if !rows.Next() || rows.Scan(&payload) != nil {
		t.Fatal("encrypted intent missing")
	}
	rows.Close()
	if bytes.Contains(payload, []byte(req.Git.Token)) || bytes.Contains(payload, []byte(intent.LLM.ApiKey)) {
		t.Fatal("review credentials stored as plaintext")
	}
	// Missing MCAIReview support must fail explicitly before any upstream RPC.
	if err = f.client.Step(ctx); !errors.Is(err, runtimeadapter.ErrParity) {
		t.Fatalf("unsupported review was submitted or silently changed Agent: %v", err)
	}
	if f.db.Task.GetX(ctx, created.TaskID).Status != consts.TaskStatusError {
		t.Fatal("unsupported review remained pending without an explicit error")
	}
	for _, secret := range []string{req.Git.Token, intent.LLM.ApiKey} {
		if strings.Contains(f.logs.String(), secret) {
			t.Fatal("review admission logged credentials")
		}
	}
}

func TestGitTaskAdmissionFailureRollsBackProductAndCommand(t *testing.T) {
	f := newReviewFixture(t)
	f.client.after = func(context.Context, taskflow.SQLExecutor, taskflow.CreateTaskReq) error {
		return errors.New("upstream diagnostic review-fixture-git-secret")
	}
	if _, err := f.u.Create(context.Background(), reviewRequest()); err == nil || strings.Contains(err.Error(), "review-fixture-git-secret") {
		t.Fatal("admission failure acknowledged or leaked diagnostics")
	}
	for _, table := range []string{"tasks", "git_tasks", "virtualmachines", "task_virtualmachines", "runtime_task_intents", "runtime_commands"} {
		if reviewSQLCount(t, f.db, table) != 0 {
			t.Fatalf("failed admission committed %s", table)
		}
	}
	if err := f.client.Step(context.Background()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("rollback left an executable preparation")
	}
}

func TestGitTaskLegacyAdmissionRetainsRedisContract(t *testing.T) {
	for _, failRedis := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "storage_failure"}[failRedis], func(t *testing.T) {
			f := newReviewFixture(t)
			f.client.legacy = true
			if failRedis {
				f.redis.Close()
			}
			created, err := f.u.Create(context.Background(), reviewRequest())
			if failRedis {
				if err == nil || reviewSQLCount(t, f.db, "tasks") != 0 {
					t.Fatal("lost legacy request acknowledged as a created task")
				}
				return
			}
			if err != nil || !f.redis.Exists("task:create_req:"+created.TaskID.String()) {
				t.Fatal("legacy request contract changed")
			}
			if reviewSQLCount(t, f.db, "runtime_task_intents") != 0 || reviewSQLCount(t, f.db, "runtime_commands") != 0 {
				t.Fatal("legacy task was rerouted into agent-compose")
			}
		})
	}
}

func TestGitTaskDeferredReviewRejectsBeforeProductOrEnvironmentCreation(t *testing.T) {
	f := newReviewFixture(t)
	f.u.taskflow = f.client.Client // Exercise the actual gate, without the fixture bypass.
	if _, err := f.u.Create(context.Background(), reviewRequest()); !errors.Is(err, runtimeadapter.ErrReviewDeferred) {
		t.Fatalf("deferred review admission: %v", err)
	}
	for _, table := range []string{"tasks", "git_tasks", "virtualmachines", "runtime_environments", "runtime_task_intents", "runtime_commands"} {
		if reviewSQLCount(t, f.db, table) != 0 {
			t.Fatalf("deferred review left a row in %s", table)
		}
	}
	if len(f.redis.Keys()) != 0 {
		t.Fatal("deferred review initialized lifecycle or a Redis request")
	}
}
