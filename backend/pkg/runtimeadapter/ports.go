package runtimeadapter

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

//go:embed guest/ports.py
var portScript []byte

type portClient struct{ c *Client }
type portForward struct {
	ID, EnvironmentID string
	Port              int32
	AccessMode        string
	TaskID            sql.NullString
	Whitelist         []string
	Revision          int64
	Active            bool
	Created           time.Time
}

func (p *portClient) route(ctx context.Context, id string) (Environment, *Engine, taskflow.PortForwarder, error) {
	env, engine, err := p.c.environment(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		if p.c.legacy == nil {
			return env, nil, nil, ErrLegacyUnavailable
		}
		return env, nil, p.c.legacy.PortForwarder(), nil
	}
	if err != nil {
		return env, nil, nil, err
	}
	if !p.c.PreviewEnabled() {
		return env, nil, nil, errors.New("runtime preview gateway is not configured")
	}
	return env, engine, nil, nil
}

func (c *Client) portInfo(f portForward) *taskflow.PortForwardInfo {
	u := c.previewURL(f)
	var accessURL *string
	if u != nil {
		value := u.String()
		accessURL = &value
	}
	id := f.ID
	info := &taskflow.PortForwardInfo{Port: f.Port, Status: "connected", ForwardID: &id, AccessURL: accessURL, CreatedAt: f.Created.Unix(), Success: f.Active && accessURL != nil, WhitelistIPs: f.Whitelist, AccessMode: f.AccessMode, DiscoveryState: "ready"}
	if f.AccessMode == "ip_allowlist" && len(f.Whitelist) == 0 {
		info.Success = false
		info.ErrorMessage = "Preview IP whitelist is empty; allow an IP before opening the website"
	}
	if !f.Active {
		info.Status = "closed"
		info.AccessURL = nil
		info.ErrorMessage = "Preview access is closed"
	}
	return info
}

// EnvironmentPreview reads stored settings, including a closed mapping. Callers
// must first authorize management of this environment. It never opens a port.
func (c *Client) EnvironmentPreview(ctx context.Context, environmentID string, port int32) (*taskflow.PortForwardInfo, error) {
	f, err := scanForward(c.ledger.db.QueryRowContext(ctx, "SELECT "+forwardColumns+" FROM runtime_port_forwards WHERE environment_id=$1 AND port=$2", environmentID, port))
	if err != nil {
		return nil, err
	}
	return c.portInfo(f), nil
}

func scanForward(row interface{ Scan(...any) error }) (portForward, error) {
	var f portForward
	var raw []byte
	err := row.Scan(&f.ID, &f.EnvironmentID, &f.Port, &raw, &f.Revision, &f.Active, &f.Created, &f.AccessMode, &f.TaskID)
	if err == nil {
		err = json.Unmarshal(raw, &f.Whitelist)
	}
	return f, err
}

const forwardColumns = "id,environment_id,port,whitelist,revision,active,created_at,access_mode,task_id"

func (c *Client) forward(ctx context.Context, id string) (portForward, error) {
	if _, err := uuid.Parse(id); err != nil {
		return portForward{}, errors.New("invalid port forward ID")
	}
	return scanForward(c.ledger.db.QueryRowContext(ctx, "SELECT "+forwardColumns+" FROM runtime_port_forwards WHERE id=$1 AND active", id))
}

// Resolve ownership from durable runtime relationships, never from a request's
// task ID. Lock intent before task (the same order as continuation/restore), so
// deletion and restore cannot race an allocation back into an obsolete runtime.
func previewTaskForEnvironmentTx(ctx context.Context, tx *sql.Tx, environmentID string) (sql.NullString, error) {
	rows, err := tx.QueryContext(ctx, `SELECT task_id FROM runtime_task_intents WHERE environment_id=$1
UNION SELECT task_id FROM runtime_commands WHERE environment_id=$1 AND task_id IS NOT NULL`, environmentID)
	if err != nil {
		return sql.NullString{}, err
	}
	var task sql.NullString
	for rows.Next() {
		if task.Valid {
			rows.Close()
			return task, errors.New("preview environment has conflicting task relationships")
		}
		if err = rows.Scan(&task); err != nil {
			rows.Close()
			return task, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || !task.Valid {
		return task, err
	}
	var current string
	var deleting bool
	if err = tx.QueryRowContext(ctx, `SELECT environment_id,deletion_requested FROM runtime_task_intents WHERE task_id=$1 FOR SHARE`, task.String).Scan(&current, &deleting); err != nil {
		return task, err
	}
	if deleting || current != environmentID {
		return task, errors.New("preview task is being deleted or its environment has changed")
	}
	var deleted sql.NullTime
	if err = tx.QueryRowContext(ctx, `SELECT deleted_at FROM tasks WHERE id=$1 FOR SHARE`, task.String).Scan(&deleted); err != nil {
		return task, err
	}
	if deleted.Valid {
		return task, errors.New("preview task has been deleted")
	}
	return task, nil
}

type detectedPort struct {
	Port      int32  `json:"port"`
	Process   string `json:"process"`
	Protected bool   `json:"protected"`
}

func (c *Client) discoverPreviewPorts(ctx context.Context, env Environment, engine *Engine) ([]detectedPort, error) {
	var ports []detectedPort
	if env.State != "online" || env.SandboxID == "" {
		return ports, nil
	}
	result, err := engine.execute(ctx, env.SandboxID, "python3 -c \"import base64;exec(base64.b64decode('"+base64.StdEncoding.EncodeToString(portScript)+"'))\"", 1<<20)
	if err != nil {
		return nil, err
	}
	if json.Unmarshal([]byte(result), &ports) != nil {
		return nil, errors.New("invalid Guest port list")
	}
	return ports, nil
}

// HTTP status codes belong to the application: even a 404/500 proves that the
// tunnel reaches an HTTP service. Redirects are never followed to other hosts.
func probePreviewHTTP(ctx context.Context, engine *Engine, env Environment, port int32, host string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 4 * time.Second,
		DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			return engine.dialPreview(dialCtx, ctx, env.SandboxID, port)
		}}
	defer transport.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://guest-preview/", nil)
	req.Host = host
	res, err := transport.RoundTrip(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}

func (p *portClient) List(ctx context.Context, r taskflow.ListPortforwadReq) (*taskflow.ListPortforwadResp, error) {
	// Read lifecycle before resolving a live node. A recycled workspace has no
	// engine to contact, but remains a valid task whose preview list is empty.
	env, err := p.c.ledger.Environment(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	if err = p.c.checkWorkEnvironmentAccess(ctx, env, false); err != nil {
		return nil, err
	}
	resp := &taskflow.ListPortforwadResp{RequestId: r.RequestId, Ports: []*taskflow.PortForwardInfo{}, Status: "ready"}
	if !p.c.PreviewEnabled() {
		resp.Status, resp.Message = "unconfigured", "管理员尚未配置预览域名"
		return resp, nil
	}
	switch env.State {
	case "hibernated", "stopped", "deleted":
		resp.Status, resp.ScanComplete = "stopped", true
		return resp, nil
	case "online":
	default:
		resp.Status, resp.Message = "unknown", "工作空间正在保存或恢复，暂时无法确认服务状态"
		return resp, nil
	}
	_, engine, err := p.c.environment(ctx, env.ID)
	if err != nil || env.SandboxID == "" {
		resp.Status, resp.Message = "unknown", "暂时无法连接工作空间，请稍后重试"
		return resp, nil
	}
	discovered, err := p.c.discoverHTTPServices(ctx, env, engine)
	if err != nil {
		resp.Status, resp.Message = "unknown", "暂时无法确认服务状态，请稍后重试"
		return resp, nil
	}
	resp.ScanComplete = true
	if discovered.Uncertain {
		resp.Status, resp.Message = "unknown", "部分服务暂时无法确认，请稍后重试"
	}
	ports := map[int32]*taskflow.PortForwardInfo{}
	for _, d := range discovered.Services {
		ports[d.Port] = &taskflow.PortForwardInfo{Port: d.Port, Process: d.Process, Status: "reserved", WhitelistIPs: []string{}, AccessMode: "public", DiscoveryState: d.State}
	}
	uncertain := map[int32]detectedPort{}
	for _, d := range discovered.UncertainPorts {
		resp.UncertainPorts = append(resp.UncertainPorts, d.Port)
		uncertain[d.Port] = d
	}
	// Access policies are always fetched fresh; discovery caching cannot delay a
	// close or an allowlist change, including changes made by another backend.
	rows, err := p.c.ledger.db.QueryContext(ctx, "SELECT "+forwardColumns+" FROM runtime_port_forwards WHERE environment_id=$1", env.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		f, err := scanForward(rows)
		if err != nil {
			return nil, err
		}
		if d := ports[f.Port]; d != nil {
			info := p.c.portInfo(f)
			info.Process, info.DiscoveryState = d.Process, d.DiscoveryState
			ports[f.Port] = info
		} else if d, ok := uncertain[f.Port]; ok {
			// A stored route was confirmed HTTP when created. Preserve it on
			// transient failures even after this backend's cache was restarted.
			info := p.c.portInfo(f)
			info.Process, info.DiscoveryState = d.Process, "unknown"
			ports[f.Port] = info
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for _, info := range ports {
		resp.Ports = append(resp.Ports, info)
		if info.DiscoveryState == "unknown" {
			resp.Status, resp.Message = "unknown", "部分服务暂时无法确认，请稍后重试"
		}
	}
	sort.Slice(resp.Ports, func(i, j int) bool { return resp.Ports[i].Port < resp.Ports[j].Port })
	return resp, nil
}

func whitelistIPs(ips []string) ([]string, error) {
	if len(ips) > 64 {
		return nil, errors.New("port whitelist permits at most 64 IP addresses")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, value := range ips {
		ip := net.ParseIP(strings.TrimSpace(value))
		if ip == nil {
			return nil, errors.New("invalid port whitelist IP")
		}
		s := ip.String()
		if !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	sort.Strings(out)
	return out, nil
}

func (p *portClient) Create(ctx context.Context, r taskflow.CreatePortForward) (*taskflow.PortForwardInfo, error) {
	return p.create(ctx, r)
}

// AI requests can create a public route or reuse its current policy, but cannot
// reopen access explicitly closed by the employee.
func (c *Client) RequestEnvironmentPreview(ctx context.Context, environmentID, ownerID string, port int32) (*taskflow.PortForwardInfo, error) {
	return (&portClient{c: c}).create(ctx, taskflow.CreatePortForward{ID: environmentID, UserID: ownerID, LocalPort: port})
}

func (p *portClient) create(ctx context.Context, r taskflow.CreatePortForward) (*taskflow.PortForwardInfo, error) {
	env, engine, old, err := p.route(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	if old != nil {
		return old.Create(ctx, r)
	}
	if env.State != "online" || env.SandboxID == "" {
		return nil, errors.New("预览暂不可用，请通过续聊恢复工作空间并启动网页服务")
	}
	if r.UserID == "" {
		return nil, errors.New("port forward requires an environment owner")
	}
	if err = authorize(env, r.UserID, ""); err != nil {
		return nil, err
	}
	if r.LocalPort < 1 || r.LocalPort > 65535 {
		return nil, errors.New("invalid preview port")
	}
	detected, err := p.c.discoverPreviewPorts(ctx, env, engine)
	if err != nil {
		return nil, err
	}
	listening := false
	for _, d := range detected {
		if d.Port == r.LocalPort {
			if d.Protected {
				return nil, errors.New("platform internal ports cannot be previewed")
			}
			listening = true
		}
	}
	if !listening {
		return nil, errors.New("preview port is not listening; start the background service first")
	}
	base, _ := url.Parse(p.c.preview.BaseURL)
	if err = probePreviewHTTP(ctx, engine, env, r.LocalPort, base.Host); err != nil {
		return nil, errors.New("暂时无法确认 HTTP 服务，请检查服务日志后重试")
	}
	tx, err := p.c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	task, err := previewTaskForEnvironmentTx(ctx, tx, env.ID)
	if err != nil {
		return nil, err
	}
	// Task/intent precede environment, then the per-task route lock, matching
	// deletion and restore. No external ports are allocated.
	var state string
	if err = tx.QueryRowContext(ctx, "SELECT state FROM runtime_environments WHERE id=$1 FOR SHARE", env.ID).Scan(&state); err != nil {
		return nil, err
	}
	if state != "online" {
		return nil, errors.New("预览暂不可用，请稍后重试")
	}
	lookup, key := "environment_id=$1 AND task_id IS NULL", env.ID
	if task.Valid {
		lookup, key = "task_id=$1", task.String
	}
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 54))", key); err != nil {
		return nil, err
	}
	f, err := scanForward(tx.QueryRowContext(ctx, "SELECT "+forwardColumns+" FROM runtime_port_forwards WHERE "+lookup+" AND port=$2 FOR UPDATE", key, r.LocalPort))
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	ips, err := whitelistIPs(r.WhitelistIPs)
	if err != nil {
		return nil, err
	}
	mode := r.AccessMode
	if mode == "" {
		mode = "public"
	}
	if mode != "public" && mode != "ip_allowlist" {
		return nil, errors.New("invalid preview access mode")
	}
	if !exists {
		f = portForward{ID: uuid.NewString(), EnvironmentID: env.ID, Port: r.LocalPort, TaskID: task}
	} else if !f.Active && !r.Reopen {
		return nil, errors.New("预览访问已关闭，请在预览面板中重新开放")
	}
	// Reusing/reopening a route never overwrites access settings. The separate
	// policy update is the only way to change public/allowlist permissions.
	if exists {
		f, err = scanForward(tx.QueryRowContext(ctx, `UPDATE runtime_port_forwards SET environment_id=$2,
active=true,revision=revision+CASE WHEN active AND environment_id=$2 THEN 0 ELSE 1 END
WHERE id=$1 RETURNING `+forwardColumns, f.ID, env.ID))
	} else {
		f, err = scanForward(tx.QueryRowContext(ctx, "INSERT INTO runtime_port_forwards(id,environment_id,port,whitelist,access_mode,task_id) VALUES($1,$2,$3,$4,$5,$6) RETURNING "+forwardColumns, f.ID, env.ID, r.LocalPort, string(mustJSON(ips)), mode, task))
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return p.c.portInfo(f), nil
}

func (p *portClient) Close(ctx context.Context, r taskflow.ClosePortForward) error {
	_, _, old, err := p.route(ctx, r.ID)
	if err != nil {
		return err
	}
	if old != nil {
		return old.Close(ctx, r)
	}
	if _, err = uuid.Parse(r.ForwardID); err != nil {
		return errors.New("invalid port forward ID")
	}
	res, err := p.c.ledger.db.ExecContext(ctx, "UPDATE runtime_port_forwards SET active=false,revision=revision+1 WHERE id=$1 AND environment_id=$2 AND active", r.ForwardID, r.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("port forward not found")
	}
	return nil
}

func (p *portClient) Update(ctx context.Context, r taskflow.UpdatePortForward) (*taskflow.PortForwardInfo, error) {
	_, _, old, err := p.route(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	if old != nil {
		return old.Update(ctx, r)
	}
	if _, err = uuid.Parse(r.ForwardID); err != nil {
		return nil, errors.New("invalid port forward ID")
	}
	ips, err := whitelistIPs(r.WhitelistIPs)
	if err != nil {
		return nil, err
	}
	if r.AccessMode != "public" && r.AccessMode != "ip_allowlist" {
		return nil, errors.New("invalid preview access mode")
	}
	f, err := scanForward(p.c.ledger.db.QueryRowContext(ctx, "UPDATE runtime_port_forwards SET whitelist=$3,access_mode=$4,revision=revision+CASE WHEN whitelist=$3::jsonb AND access_mode=$4 THEN 0 ELSE 1 END WHERE id=$1 AND environment_id=$2 RETURNING "+forwardColumns, r.ForwardID, r.ID, string(mustJSON(ips)), r.AccessMode))
	if err != nil {
		return nil, err
	}
	return p.c.portInfo(f), nil
}
