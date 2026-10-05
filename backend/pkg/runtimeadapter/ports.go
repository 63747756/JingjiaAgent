package runtimeadapter

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
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
	if p.c.preview.BaseURL == "" {
		return env, nil, nil, errors.New("runtime preview gateway is not configured")
	}
	return env, engine, nil, nil
}

func (c *Client) portInfo(f portForward) *taskflow.PortForwardInfo {
	url := strings.TrimRight(c.callbackURL, "/") + "/api/v1/runtime/previews/" + f.ID
	id := f.ID
	return &taskflow.PortForwardInfo{Port: f.Port, Status: "connected", ForwardID: &id, AccessURL: &url, CreatedAt: f.Created.Unix(), Success: true, WhitelistIPs: f.Whitelist}
}

func scanForward(row interface{ Scan(...any) error }) (portForward, error) {
	var f portForward
	var raw []byte
	err := row.Scan(&f.ID, &f.EnvironmentID, &f.Port, &raw, &f.Revision, &f.Active, &f.Created)
	if err == nil {
		err = json.Unmarshal(raw, &f.Whitelist)
	}
	return f, err
}

const forwardColumns = "id,environment_id,port,whitelist,revision,active,created_at"

func (c *Client) forward(ctx context.Context, id string) (portForward, error) {
	if _, err := uuid.Parse(id); err != nil {
		return portForward{}, errors.New("invalid port forward ID")
	}
	return scanForward(c.ledger.db.QueryRowContext(ctx, "SELECT "+forwardColumns+" FROM runtime_port_forwards WHERE id=$1 AND active", id))
}

func (p *portClient) List(ctx context.Context, r taskflow.ListPortforwadReq) (*taskflow.ListPortforwadResp, error) {
	env, engine, old, err := p.route(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	if old != nil {
		return old.List(ctx, r)
	}
	var detected []struct {
		Port    int32  `json:"port"`
		Process string `json:"process"`
	}
	if env.State == "online" && env.SandboxID != "" {
		result, err := engine.execute(ctx, env.SandboxID, "python3 -c \"import base64;exec(base64.b64decode('"+base64.StdEncoding.EncodeToString(portScript)+"'))\"", 1<<20)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(result), &detected); err != nil {
			return nil, errors.New("invalid Guest port list")
		}
	}
	ports := map[int32]*taskflow.PortForwardInfo{}
	for _, d := range detected {
		if d.Port > 0 && d.Port <= 65535 {
			ports[d.Port] = &taskflow.PortForwardInfo{Port: d.Port, Process: d.Process, Status: "reserved", WhitelistIPs: []string{}}
		}
	}
	rows, err := p.c.ledger.db.QueryContext(ctx, "SELECT "+forwardColumns+" FROM runtime_port_forwards WHERE environment_id=$1 AND active", env.ID)
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
	env, _, old, err := p.route(ctx, r.ID)
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
	ips, err := whitelistIPs(r.WhitelistIPs)
	if err != nil {
		return nil, err
	}
	f, err := scanForward(p.c.ledger.db.QueryRowContext(ctx, "INSERT INTO runtime_port_forwards(id,environment_id,port,whitelist) VALUES($1,$2,$3,$4) ON CONFLICT(environment_id,port) DO UPDATE SET whitelist=excluded.whitelist,active=true,revision=runtime_port_forwards.revision+1 RETURNING "+forwardColumns, uuid.NewString(), env.ID, r.LocalPort, string(mustJSON(ips))))
	if err != nil {
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
	f, err := scanForward(p.c.ledger.db.QueryRowContext(ctx, "UPDATE runtime_port_forwards SET whitelist=$3,revision=revision+1 WHERE id=$1 AND environment_id=$2 AND active RETURNING "+forwardColumns, r.ForwardID, r.ID, string(mustJSON(ips))))
	if err != nil {
		return nil, err
	}
	return p.c.portInfo(f), nil
}
