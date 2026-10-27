package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type guestRuntimeClient struct {
	client  *http.Client
	baseURL string
	token   string
}

func newGuestRuntimeClient(s *deployment) (*guestRuntimeClient, error) {
	if s.Mode != "single" {
		return nil, errors.New("当前部署没有本机运行时，不能恢复本机工作空间")
	}
	ids, err := output("docker", composeArgs(s, "ps", "-q", "runtime-proxy")...)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(ids))
	if len(fields) != 1 {
		return nil, errors.New("本机运行时代理未运行，已保留工作空间恢复清单")
	}
	b, err := output("docker", "inspect", fields[0])
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Config          struct{ Labels map[string]string }
		State           struct{ Running bool }
		NetworkSettings struct {
			Networks map[string]struct{ IPAddress string }
		}
	}
	if json.Unmarshal(b, &rows) != nil || len(rows) != 1 || !rows[0].State.Running ||
		rows[0].Config.Labels["com.docker.compose.project"] != s.Project ||
		rows[0].Config.Labels["com.docker.compose.service"] != "runtime-proxy" {
		return nil, errors.New("本机运行时代理身份不匹配")
	}
	ip := net.ParseIP(rows[0].NetworkSettings.Networks[s.Project+"_sandbox_direct"].IPAddress)
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() {
		return nil, errors.New("本机运行时代理缺少受控 Docker 网络地址")
	}
	ca, err := os.ReadFile(filepath.Join(s.Root, "tls/ca.crt"))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("invalid installation CA")
	}
	token, err := os.ReadFile(filepath.Join(s.Root, "secrets/node.token"))
	if err != nil {
		return nil, err
	}
	if len(token) != 64 {
		return nil, errors.New("invalid node token")
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "runtime-proxy", MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), "7411"))
		},
	}
	return &guestRuntimeClient{
		client: &http.Client{Transport: transport, Timeout: 150 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		baseURL: "https://runtime-proxy:7411", token: string(token),
	}, nil
}

func (c *guestRuntimeClient) call(method, id string) (string, error) {
	body, err := json.Marshal(map[string]string{"sandboxId": id})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/agentcompose.v2.SandboxService/"+method, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("运行时 %s 请求失败: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Do not echo runtime payloads or credentials into installer logs.
		return "", fmt.Errorf("运行时 %s 返回 HTTP %d，请检查运行时日志", method, resp.StatusCode)
	}
	var result struct {
		Sandbox struct{ SandboxID, Status, Driver, WorkspaceReclamationState, StoppedRuntimeState string }
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return "", fmt.Errorf("运行时 %s 响应无效", method)
	}
	if result.Sandbox.SandboxID != id || result.Sandbox.Driver != "docker" {
		return "", fmt.Errorf("运行时 %s 返回的工作空间身份不匹配", method)
	}
	if result.Sandbox.WorkspaceReclamationState != "" && result.Sandbox.WorkspaceReclamationState != "WORKSPACE_RECLAMATION_STATE_UNSPECIFIED" {
		return "", errors.New("工作空间已进入回收流程，不能由维护操作恢复")
	}
	if result.Sandbox.StoppedRuntimeState != "" && result.Sandbox.StoppedRuntimeState != "retained" {
		return "", errors.New("工作空间容器已进入释放流程，不能由维护操作重建")
	}
	return result.Sandbox.Status, nil
}

func (c *guestRuntimeClient) resume(id string) error {
	status, err := c.call("GetSandbox", id)
	if err != nil {
		return err
	}
	switch status {
	case "SANDBOX_STATUS_STOPPED":
		if status, err = c.call("ResumeSandbox", id); err != nil {
			return err
		}
	case "SANDBOX_STATUS_RUNNING":
		// A retry must preserve the existing container and its live services.
	default:
		return fmt.Errorf("工作空间当前状态为 %s，不能自动恢复", status)
	}
	if status != "SANDBOX_STATUS_RUNNING" {
		return errors.New("运行时尚未确认工作空间正在运行")
	}
	status, err = c.call("GetSandbox", id)
	if err != nil {
		return err
	}
	if status != "SANDBOX_STATUS_RUNNING" {
		return errors.New("工作空间恢复后未保持运行状态")
	}
	return nil
}
