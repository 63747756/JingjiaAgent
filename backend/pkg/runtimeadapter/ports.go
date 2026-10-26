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

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

//go:embed guest/ports.py
var portScript []byte

type portClient struct{ c *Client }
type portForward struct {
	ID, EnvironmentID string
	Port              int32
	PublicPort        sql.NullInt32
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
	info := &taskflow.PortForwardInfo{Port: f.Port, Status: "connected", ForwardID: &id, AccessURL: accessURL, CreatedAt: f.Created.Unix(), Success: f.Active && accessURL != nil, WhitelistIPs: f.Whitelist}
	if len(f.Whitelist) == 0 {
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
	f, err := scanForward(c.ledger.db.QueryRowContext(ctx, "SELECT "+forwardColumns+" FROM runtime_port_forwards WHERE environment_id=$1 AND port=$2 AND released_at IS NULL", environmentID, port))
	if err != nil {
		return nil, err
	}
	return c.portInfo(f), nil
}

func scanForward(row interface{ Scan(...any) error }) (portForward, error) {
	var f portForward
	var raw []byte
	err := row.Scan(&f.ID, &f.EnvironmentID, &f.Port, &raw, &f.Revision, &f.Active, &f.Created, &f.PublicPort, &f.TaskID)
	if err == nil {
		err = json.Unmarshal(raw, &f.Whitelist)
	}
	return f, err
}

const forwardColumns = "id,environment_id,port,whitelist,revision,active,created_at,public_port,task_id"

func (c *Client) forward(ctx context.Context, id string) (portForward, error) {
	if _, err := uuid.Parse(id); err != nil {
		return portForward{}, errors.New("invalid port forward ID")
	}
	return scanForward(c.ledger.db.QueryRowContext(ctx, "SELECT "+forwardColumns+" FROM runtime_port_forwards WHERE id=$1 AND active AND released_at IS NULL", id))
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
		return errors.New("preview port is not serving HTTP; check the background service and its logs")
	}
	res.Body.Close()
	return nil
}

func (p *portClient) List(ctx context.Context, r taskflow.ListPortforwadReq) (*taskflow.ListPortforwadResp, error) {
	env, engine, old, err := p.route(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	if old != nil {
		return old.List(ctx, r)
	}
	detected, err := p.c.discoverPreviewPorts(ctx, env, engine)
	if err != nil {
		return nil, err
	}
	ports := map[int32]*taskflow.PortForwardInfo{}
	for _, d := range detected {
		if d.Port > 0 && d.Port <= 65535 && !d.Protected {
			ports[d.Port] = &taskflow.PortForwardInfo{Port: d.Port, Process: d.Process, Status: "reserved", WhitelistIPs: []string{}}
		}
	}
	rows, err := p.c.ledger.db.QueryContext(ctx, "SELECT "+forwardColumns+" FROM runtime_port_forwards WHERE environment_id=$1 AND released_at IS NULL", env.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		f, err := scanForward(rows)
		if err != nil {
			return nil, err
		}
		info := p.c.portInfo(f)
		if d := ports[f.Port]; d != nil {
			info.Process = d.Process
		} else {
			info.Status = "reserved"
			info.ErrorMessage = "Port is not listening"
			info.AccessURL = nil
			info.Success = false
		}
		if !f.Active {
			info.Status = "closed"
			info.AccessURL = nil
			info.Success = false
			info.ErrorMessage = "Preview access is closed"
		}
		ports[f.Port] = info
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	resp := &taskflow.ListPortforwadResp{RequestId: r.RequestId, Ports: []*taskflow.PortForwardInfo{}}
	for _, info := range ports {
		resp.Ports = append(resp.Ports, info)
	}
	sort.Slice(resp.Ports, func(i, j int) bool { return resp.Ports[i].Port < resp.Ports[j].Port })
	return resp, nil
}

func whitelistIPs(ips []string) ([]string, error) {
	if len(ips) == 0 || len(ips) > 64 {
		return nil, errors.New("port whitelist requires 1 to 64 IP addresses")
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
	return p.create(ctx, r, false)
}

// RequestEnvironmentPreview preserves an existing whitelist on repeated AI
// requests. Only a new mapping uses the employee IP observed by the backend.
func (c *Client) RequestEnvironmentPreview(ctx context.Context, environmentID, ownerID string, port int32, defaultIPs []string) (*taskflow.PortForwardInfo, error) {
	return (&portClient{c: c}).create(ctx, taskflow.CreatePortForward{ID: environmentID, UserID: ownerID, LocalPort: port, WhitelistIPs: defaultIPs}, true)
}

func (p *portClient) create(ctx context.Context, r taskflow.CreatePortForward, preserveWhitelist bool) (*taskflow.PortForwardInfo, error) {
	env, engine, old, err := p.route(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	if old != nil {
		return old.Create(ctx, r)
	}
	if env.State != "online" || env.SandboxID == "" {
		return nil, errors.New("preview environment is not ready")
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
		return nil, err
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
	// One allocator across backend instances. Task/intent precede environment,
	// which precedes the mapping, matching deletion and restore lock order.
	var state string
	if err = tx.QueryRowContext(ctx, "SELECT state FROM runtime_environments WHERE id=$1 FOR SHARE", env.ID).Scan(&state); err != nil {
		return nil, err
	}
	if state != "online" {
		return nil, errors.New("preview environment is not ready")
	}
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(1297040467, 51)"); err != nil {
		return nil, err
	}
	lookup, key := "environment_id=$1 AND task_id IS NULL", env.ID
	if task.Valid {
		lookup, key = "task_id=$1", task.String
	}
	f, err := scanForward(tx.QueryRowContext(ctx, "SELECT "+forwardColumns+" FROM runtime_port_forwards WHERE "+lookup+" AND port=$2 FOR UPDATE", key, r.LocalPort))
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	ips := r.WhitelistIPs
	if preserveWhitelist && exists {
		ips = f.Whitelist
	}
	ips, err = whitelistIPs(ips)
	if err != nil {
		return nil, err
	}
	if !exists {
		f = portForward{ID: uuid.NewString(), EnvironmentID: env.ID, Port: r.LocalPort, TaskID: task}
	}
	if p.c.preview.EffectiveMode() == "port" {
		first, last, rangeErr := config.PreviewPortRange(p.c.preview.PortRange)
		if rangeErr != nil {
			return nil, rangeErr
		}
		if f.PublicPort.Valid && (int(f.PublicPort.Int32) < first || int(f.PublicPort.Int32) > last) {
			return nil, errors.New("existing preview address is outside configured port range; restore the configured range")
		}
		if !f.PublicPort.Valid {
			if _, err = tx.ExecContext(ctx, "UPDATE runtime_port_forwards SET public_port=NULL WHERE released_at < now()-interval '24 hours' AND public_port IS NOT NULL"); err != nil {
				return nil, err
			}
			err = tx.QueryRowContext(ctx, "SELECT p FROM generate_series($1::integer,$2::integer) p WHERE NOT EXISTS(SELECT 1 FROM runtime_port_forwards f WHERE f.public_port=p) ORDER BY p LIMIT 1", first, last).Scan(&f.PublicPort.Int32)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, errors.New("preview port pool is full; remove unused tasks or increase the configured port range")
			}
			if err != nil {
				return nil, err
			}
			f.PublicPort.Valid = true
		}
	}
	// The allocator and task locks serialize insertion, including on different
	// backend instances. Update the existing ID rather than creating a generation.
	if exists {
		f, err = scanForward(tx.QueryRowContext(ctx, `UPDATE runtime_port_forwards SET environment_id=$2,
whitelist=$3,public_port=$4,active=true,released_at=NULL,
revision=revision+CASE WHEN active AND whitelist=$3::jsonb AND environment_id=$2 THEN 0 ELSE 1 END
WHERE id=$1 RETURNING `+forwardColumns, f.ID, env.ID, string(mustJSON(ips)), f.PublicPort))
	} else {
		f, err = scanForward(tx.QueryRowContext(ctx, "INSERT INTO runtime_port_forwards(id,environment_id,port,whitelist,public_port,task_id) VALUES($1,$2,$3,$4,$5,$6) RETURNING "+forwardColumns, f.ID, env.ID, r.LocalPort, string(mustJSON(ips)), f.PublicPort, task))
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
	// An empty owner-managed whitelist explicitly revokes everyone. Creating a
	// new mapping still requires a nonempty observed/configured whitelist.
	if len(r.WhitelistIPs) == 0 {
		ips = []string{}
		err = nil
	}
	if err != nil {
		return nil, err
	}
	f, err := scanForward(p.c.ledger.db.QueryRowContext(ctx, "UPDATE runtime_port_forwards SET whitelist=$3,revision=revision+CASE WHEN whitelist=$3::jsonb THEN 0 ELSE 1 END WHERE id=$1 AND environment_id=$2 AND released_at IS NULL RETURNING "+forwardColumns, r.ForwardID, r.ID, string(mustJSON(ips))))
	if err != nil {
		return nil, err
	}
	return p.c.portInfo(f), nil
}
