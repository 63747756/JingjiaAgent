package runtimeadapter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/config"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type Engine struct {
	projects       rpc.ProjectServiceClient
	runs           rpc.RunServiceClient
	sandboxes      rpc.SandboxServiceClient
	exec           rpc.ExecServiceClient
	nodeURL, token string
	tlsConfig      *tls.Config
	nodeHTTP       *http.Client
}

type authTransport struct {
	base  http.RoundTripper
	token string
}

func (t authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header = req.Header.Clone()
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(r)
}
func NewEngine(node config.RuntimeNode) (*Engine, error) {
	token, err := os.ReadFile(node.TokenFile)
	if err != nil {
		return nil, errors.New("cannot read runtime node token file")
	}
	secret := strings.TrimSpace(string(token))
	if secret == "" || strings.ContainsAny(secret, "\r\n") {
		return nil, errors.New("invalid runtime node token file")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if node.CAFile != "" {
		b, err := os.ReadFile(node.CAFile)
		if err != nil {
			return nil, errors.New("cannot read runtime CA file")
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(b) {
			return nil, errors.New("invalid runtime CA file")
		}
		tlsConfig.RootCAs = pool
	}
	t := &http.Transport{TLSClientConfig: tlsConfig, MaxIdleConnsPerHost: 16, IdleConnTimeout: time.Minute, ResponseHeaderTimeout: 30 * time.Second}
	h := &http.Client{Transport: authTransport{t, secret}, Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base := strings.TrimRight(node.URL, "/")
	return &Engine{projects: rpc.NewProjectServiceClient(h, base, connect.WithProtoJSON()), runs: rpc.NewRunServiceClient(h, base, connect.WithProtoJSON()), sandboxes: rpc.NewSandboxServiceClient(h, base, connect.WithProtoJSON()), exec: rpc.NewExecServiceClient(h, base, connect.WithProtoJSON()), nodeURL: base, token: secret, tlsConfig: tlsConfig, nodeHTTP: h}, nil
}

func (e *Engine) execute(ctx context.Context, sandbox, command string, max uint32) (string, error) {
	r, err := e.exec.Exec(ctx, connect.NewRequest(&v2.ExecRequest{Target: &v2.ExecRequest_SandboxId{SandboxId: sandbox}, Command: &v2.ExecCommand{Command: "sh", Args: []string{"-c", command}}, Cwd: "/workspace", TimeoutMs: 45000, MaxOutputBytes: max}))
	if err != nil {
		return "", err
	}
	if r.Msg.Result == nil || r.Msg.Result.ExitCode != 0 {
		return "", errors.New("runtime guest command failed")
	}
	if r.Msg.Result.StdoutTruncated || r.Msg.Result.OutputTruncated {
		return "", errors.New("runtime guest output exceeded limit")
	}
	return r.Msg.Result.Stdout, nil
}
