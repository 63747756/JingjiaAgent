// Package runtimeadapter keeps the existing Taskflow product boundary while
// routing each environment to the backend selected when it was created.
package runtimeadapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	_ "github.com/lib/pq"
)

var ErrParity = errors.New("agent-compose Guest compatibility is not yet accepted for this operation")
var ErrLegacyUnavailable = errors.New("legacy Taskflow backend is unavailable; existing environments cannot be rerouted")
var ErrReviewDeferred = errors.New("automatic PR/MR review is deferred for the agent-compose backend")

type Client struct {
	nodeStateMu                sync.RWMutex
	nodeVerified               map[string]time.Time
	ledger                     *Ledger
	legacy                     taskflow.Clienter
	backend                    string
	engines                    map[string]*Engine
	nodes                      map[string]config.RuntimeNode
	logger                     *slog.Logger
	callbackURL, callbackToken string
	poll                       time.Duration
	http                       *http.Client
	objectStorage              config.ObjectStorageConfig
	builtinMCPURL, agentMCPURL string
	gitCredentialURL           string
	preview                    config.RuntimePreview
	registry                   HostRegistry
}

func NewClient(cfg *config.Config, logger *slog.Logger) (*Client, error) {
	if err := cfg.Runtime.Validate(); err != nil {
		return nil, err
	}
	if err := cfg.Runtime.Preview.Validate(cfg.Server.BaseURL); err != nil {
		return nil, err
	}
	key, err := LoadKey(cfg.Runtime.PayloadKeyFile)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("postgres", cfg.Database.Master)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	ledger, err := NewLedger(db, key)
	if err != nil {
		db.Close()
		return nil, err
	}
	ledger.capacity = cfg.Runtime.Capacity
	c := &Client{ledger: ledger, backend: cfg.Runtime.Backend, engines: map[string]*Engine{}, nodes: map[string]config.RuntimeNode{}, logger: logger, callbackURL: cfg.Server.BaseURL, callbackToken: cfg.TaskFlow.CallbackToken, http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	c.objectStorage = cfg.ObjectStorage
	c.builtinMCPURL = strings.TrimRight(cfg.Server.BaseURL, "/") + "/mcp"
	c.agentMCPURL = cfg.Runtime.MCPURL
	base := cfg.LLMProxy.BaseURL
	if base == "" {
		base = cfg.Server.BaseURL
	}
	c.gitCredentialURL = strings.TrimRight(base, "/") + "/api/v1/runtime/git-credential"
	c.preview = cfg.Runtime.Preview
	c.poll, _ = time.ParseDuration(cfg.Runtime.PollInterval)
	if os.Getenv("TASKFLOW_SERVER") != "" {
		c.legacy = taskflow.NewClient(taskflow.WithDebug(cfg.Debug), taskflow.WithLogger(logger))
	}
	for _, node := range cfg.Runtime.Nodes {
		e, err := NewEngine(node)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("runtime node %s: %w", node.ID, err)
		}
		c.engines[node.ID] = e
		c.nodes[node.ID] = node
	}
	if c.callbackToken == "" || c.callbackURL == "" {
		db.Close()
		return nil, errors.New("runtime callback token and server.base_url are required")
	}
	return c, nil
}

func (c *Client) Close() error { return c.ledger.db.Close() }

func (c *Client) CheckNewReview(context.Context) error {
	if c.backend == "agent_compose" {
		return ErrReviewDeferred
	}
	return nil
}
func (c *Client) environment(ctx context.Context, id string) (Environment, *Engine, error) {
	e, err := c.ledger.Environment(ctx, id)
	if err != nil {
		return e, nil, err
	}
	if e.State == "deleted" {
		return e, nil, errors.New("virtual_machine not found")
	}
	engine := c.engines[e.NodeID]
	if engine == nil {
		return e, nil, errors.New("recorded runtime node is unavailable")
	}
	if c.registry != nil {
		online, err := c.nodeReady(ctx, e.NodeID)
		if err != nil {
			return e, nil, err
		}
		if !online {
			return e, nil, errors.New("recorded runtime node is not ready")
		}
	}
	return e, engine, nil
}
func (c *Client) legacyVM() (taskflow.VirtualMachiner, error) {
	if c.legacy == nil {
		return nil, ErrLegacyUnavailable
	}
	return c.legacy.VirtualMachiner(), nil
}
func (c *Client) legacyTask() (taskflow.TaskManager, error) {
	if c.legacy == nil {
		return nil, ErrLegacyUnavailable
	}
	return c.legacy.TaskManager(), nil
}
func (c *Client) VirtualMachiner() taskflow.VirtualMachiner { return &vmClient{c} }
func (c *Client) TaskManager() taskflow.TaskManager         { return &taskClient{c} }
func (c *Client) FileManager() taskflow.FileManager         { return &fileClient{c} }
func (c *Client) Host() taskflow.Hoster                     { return &hostClient{c} }
func (c *Client) PortForwarder() taskflow.PortForwarder     { return &portClient{c} }

// StageTask returns false only for a legacy environment. Database failures never
// select a different backend. The prepare job cannot exist before the intent.
func (c *Client) StageTask(ctx context.Context, req taskflow.CreateTaskReq) (bool, error) {
	_, _, err := c.environment(ctx, req.VMID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	req.McpConfigs = c.runtimeMCPConfigs(req.McpConfigs)
	return true, c.ledger.Stage(ctx, req)
}

func (c *Client) StageTaskInTx(ctx context.Context, tx taskflow.SQLExecutor, req taskflow.CreateTaskReq) (bool, error) {
	if tx == nil {
		return true, errors.New("runtime admission requires the product transaction")
	}
	var state, node string
	err := scanSQLRow(ctx, tx, `SELECT state,node_id FROM runtime_environments WHERE id=$1 FOR UPDATE`, []any{req.VMID}, &state, &node)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if state == "deleted" || state == "stopping" {
		return true, errors.New("runtime environment is unavailable")
	}
	if c.engines[node] == nil {
		return true, errors.New("recorded runtime node is unavailable")
	}
	if c.registry != nil {
		if !c.nodeIsVerified(node) {
			return true, errors.New("recorded runtime node is not verified by this process")
		}
		var online bool
		err = scanSQLRow(ctx, tx, `SELECT COALESCE(ready AND last_seen_at>now()-($2 * interval '1 second'),false) FROM runtime_nodes WHERE node_id=$1`, []any{node, nodeFreshness.Seconds()}, &online)
		if err != nil {
			return true, err
		}
		if !online {
			return true, errors.New("recorded runtime node is not ready")
		}
	}
	req.McpConfigs = c.runtimeMCPConfigs(req.McpConfigs)
	return true, c.ledger.stageTx(ctx, tx, req)
}
func (c *Client) PreparedTask(ctx context.Context, id string) (*taskflow.CreateTaskReq, error) {
	r, err := c.ledger.Admission(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (c *Client) TaskLive(ctx context.Context, id string, flush bool, fn func(*taskflow.TaskChunk) error) error {
	_, err := c.ledger.EnvironmentForTask(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		if c.legacy == nil {
			return ErrLegacyUnavailable
		}
		return c.legacy.TaskLive(ctx, id, flush, fn)
	}
	if err != nil {
		return err
	}
	var after uint64
	if !flush {
		if err = c.ledger.db.QueryRowContext(ctx, `SELECT COALESCE(max(seq),0) FROM runtime_events WHERE task_id=$1`, id).Scan(&after); err != nil {
			return err
		}
	}
	return c.taskLiveAfter(ctx, id, after, false, fn)
}
func (c *Client) Stats(ctx context.Context) (*taskflow.Stats, error) {
	r := &taskflow.Stats{}
	if c.legacy != nil {
		v, err := c.legacy.Stats(ctx)
		if err != nil {
			return nil, err
		}
		if v != nil {
			*r = *v
		}
	}
	var count int
	if err := c.ledger.db.QueryRowContext(ctx, `SELECT count(*) FROM runtime_environments WHERE state='online'`).Scan(&count); err != nil {
		return nil, err
	}
	r.OnlineVMCount += count
	if err := c.ledger.db.QueryRowContext(ctx, `SELECT count(*) FROM runtime_commands WHERE operation='task' AND state='running'`).Scan(&count); err != nil {
		return nil, err
	}
	r.OnlineTaskCount += count
	return r, nil
}

var _ taskflow.Clienter = (*Client)(nil)
var _ taskflow.TransactionalCreator = (*Client)(nil)
var _ taskflow.ReviewAdmissionGate = (*Client)(nil)
