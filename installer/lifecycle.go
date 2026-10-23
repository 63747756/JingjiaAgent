package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func checkSecrets(s *deployment) error {
	for _, name := range []string{"postgres.password", "redis.password", "storage.password", "node.token", "converter.token", "mcp.token", "setup.token", "ad.key", "payload.key"} {
		b, e := os.ReadFile(filepath.Join(s.Root, "secrets", name))
		expected := 64
		if name == "ad.key" || name == "payload.key" {
			expected = 32
		}
		if e != nil || len(b) != expected {
			return fmt.Errorf("缺少已有密钥 %s；请从备份恢复，不能重新生成", name)
		}
	}
	return validateTLSSet(s)
}
func start(s *deployment) error {
	return startServices(s, false)
}
func startRendered(s *deployment) error {
	return startServices(s, true)
}
func startServices(s *deployment, recreate bool) error {
	if e := checkSecrets(s); e != nil {
		return e
	}
	if s.Phase == "recovering" {
		if e := prepareRecoveredNode(s); e != nil {
			return e
		}
	}
	args := []string{"up", "-d", "--wait", "--wait-timeout", "240"}
	if recreate {
		args = append(args, "--force-recreate")
	}
	if e := compose(s, args...); e != nil {
		return e
	}
	if e := resumeGuests(s); e != nil {
		return e
	}
	if e := waitLocalNode(s); e != nil {
		return e
	}
	if e := doctor(s); e != nil {
		return e
	}
	if s.Phase == "recovering" {
		s.Phase = "ready"
		return saveState(s)
	}
	return nil
}
func doctor(s *deployment) error {
	if e := preflightDocker(); e != nil {
		return e
	}
	if e := checkSecrets(s); e != nil {
		return e
	}
	ca, e := os.ReadFile(filepath.Join(s.Root, "tls/ca-bundle.crt"))
	if e != nil {
		return e
	}
	pool, e := x509.SystemCertPool()
	if e != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(ca) {
		return errors.New("invalid installation CA")
	}
	initialized, e := probeDeploymentAPI(s.PublicURL, pool)
	if e != nil {
		return fmt.Errorf("员工访问入口检查失败: %w", e)
	}
	if s.internalURL() != s.PublicURL {
		if _, e = probeDeploymentAPI(s.internalURL(), pool); e != nil {
			return fmt.Errorf("内部节点 HTTPS 入口检查失败: %w", e)
		}
	}
	if e = checkLocalNode(s); e != nil {
		return e
	}
	fmt.Printf("Docker、员工入口和内部 TLS/API 正常；版本 %s，管理员已初始化: %t。\n", s.Version, initialized)
	return nil
}

func probeDeploymentAPI(address string, pool *x509.CertPool) (bool, error) {
	u, e := url.Parse(address)
	if e != nil {
		return false, e
	}
	port := originPort(u)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Get(address + "/api/v1/deployment/status")
	if e != nil {
		return false, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, fmt.Errorf("API health returned %d", resp.StatusCode)
	}
	var result struct {
		Data struct {
			Enabled     bool `json:"enabled"`
			Initialized bool `json:"initialized"`
		} `json:"data"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result); e != nil || !result.Data.Enabled {
		return false, errors.New("API 初始化端点响应不正确")
	}
	return result.Data.Initialized, nil
}
func activeCommands(s *deployment) error {
	b, e := output("docker", composeArgs(s, "exec", "-T", "postgres", "psql", "-U", "postgres", "-d", "jingjiaagent", "-Atc", "SELECT count(*) FROM runtime_commands WHERE state IN ('pending','submitting','unknown','running','waiting')")...)
	if e != nil {
		return errors.New("无法确认任务执行状态，已停止维护操作")
	}
	if strings.TrimSpace(string(b)) != "0" {
		return errors.New("仍有排队或执行中的任务，请完成或取消后重试")
	}
	return nil
}
func stop(s *deployment) error {
	running, e := runningServices(s)
	if e != nil {
		return e
	}
	guests, e := ownedGuests(s, true)
	if e != nil {
		return e
	}
	if len(running) == 0 && len(guests) == 0 {
		return nil
	}
	// A partial outage must not cause maintenance to bring up additional services.
	// With live producers/workspaces, fail closed if the ledger cannot be read.
	if !running["postgres"] {
		return errors.New("数据库未运行，无法确认任务状态；请先执行 start 恢复服务后重试")
	}
	paused := []string{}
	restore := func() {
		for i := len(paused) - 1; i >= 0; i-- {
			// Start exact existing containers: Compose start may start dependencies.
			ids, err := output("docker", composeArgs(s, "ps", "-aq", paused[i])...)
			if err == nil && len(strings.Fields(string(ids))) > 0 {
				_ = execute("docker", append([]string{"start"}, strings.Fields(string(ids))...)...)
			}
		}
	}
	for _, name := range []string{"gateway", "backend"} {
		if running[name] {
			if e = compose(s, "stop", name); e != nil {
				restore()
				return e
			}
			paused = append(paused, name)
		}
		if e = activeCommands(s); e != nil {
			restore()
			return e
		}
	}
	if e = suspendGuests(s); e != nil {
		_ = resumeGuests(s)
		restore()
		return e
	}
	return compose(s, "stop")
}

func runningServices(s *deployment) (map[string]bool, error) {
	b, e := output("docker", composeArgs(s, "ps", "--status", "running", "--services")...)
	if e != nil {
		return nil, e
	}
	result := map[string]bool{}
	for _, name := range strings.Fields(string(b)) {
		result[name] = true
	}
	return result, nil
}
func backup(s *deployment, path string) error {
	if path == "" {
		return errors.New("--file is required")
	}
	unlock, e := lock(s.Root)
	if e != nil {
		return e
	}
	defer unlock()
	if e = stop(s); e != nil {
		return e
	}
	e = archiveDeployment(s, path)
	restartErr := start(s)
	if e != nil {
		return e
	}
	return restartErr
}
func archiveDeployment(s *deployment, path string) error {
	if e := preserveGuestNativeState(s); e != nil {
		return e
	}
	path, e := filepath.Abs(path)
	if e != nil {
		return e
	}
	for _, p := range []string{s.Root, s.Data} {
		if path == p || strings.HasPrefix(path, p+string(filepath.Separator)) {
			return errors.New("备份文件必须放在安装和数据目录之外")
		}
	}
	images, e := os.MkdirTemp(filepath.Dir(s.Root), ".jingjiaagent-backup-images-*")
	if e != nil {
		return e
	}
	defer os.RemoveAll(images) // Private staging directory; never a deployment path.
	if e = prepareBackupImages(s, images); e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for prefix, root := range map[string]string{"installation": s.Root, "data": s.Data, "images": images} {
		e = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if prefix == "installation" && (rel == "downloads" || rel == "release" || rel == ".operation.lock") {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if info.Mode()&os.ModeSocket != 0 {
				return nil
			}
			link := ""
			if info.Mode()&os.ModeSymlink != 0 {
				link, err = os.Readlink(path)
				if err != nil {
					return err
				}
			}
			h, err := tar.FileInfoHeader(info, link)
			if err != nil {
				return err
			}
			h.Name = prefix
			if rel != "." {
				h.Name += "/" + filepath.ToSlash(rel)
			}
			if err = tw.WriteHeader(h); err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, in)
			ce := in.Close()
			if err != nil {
				return err
			}
			return ce
		})
		if e != nil {
			return e
		}
	}
	if e = tw.Close(); e != nil {
		return e
	}
	if e = gz.Close(); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	ok = true
	fmt.Println("一致性备份已保存，包含仍在使用的历史镜像；当前发行材料使用原签名发行包恢复。")
	return nil
}
func upgrade(o options) error {
	if e := requireLinux(); e != nil {
		return e
	}
	s, e := readState(o.Root)
	if e != nil {
		return e
	}
	unlock, e := lock(s.Root)
	if e != nil {
		return e
	}
	defer unlock()
	bundle, m, e := acquire(o)
	if e != nil {
		return e
	}
	if m.Version == s.Version {
		if s.Phase != "ready" {
			return errors.New("该版本维护尚未完成，请检查日志并恢复完整备份，不能当作已升级成功")
		}
		return doctor(s)
	}
	if m.Sequence <= s.Sequence {
		return errors.New("拒绝安装更早的发行包；回退请恢复匹配版本的完整备份")
	}
	if m.DatabaseGeneration != s.DatabaseGeneration {
		return errors.New("数据库代际不兼容；需要该版本专用迁移方案，不能自动降级或跨代升级")
	}
	if e = loadImages(bundle, m, o.Offline); e != nil {
		return e
	}
	if e = stop(s); e != nil {
		return e
	}
	snapshot := filepath.Join(filepath.Dir(s.Root), s.Project+"-before-upgrade-"+time.Now().UTC().Format("20060102T150405Z")+".tar.gz")
	if e = archiveDeployment(s, snapshot); e != nil {
		_ = start(s)
		return e
	}
	s.Phase = "upgrading"
	if e = saveState(s); e != nil {
		return e
	}
	s.Version = m.Version
	s.Sequence = m.Sequence
	s.Images = m.Images
	if e = copyRelease(bundle, s, m); e != nil {
		return e
	}
	if e = render(s); e != nil {
		return e
	}
	if e = saveState(s); e != nil {
		return e
	}
	if e = startRendered(s); e != nil {
		return fmt.Errorf("升级健康检查失败；已保留诊断状态及备份 %s，请恢复完整备份，不能只回退镜像: %w", snapshot, e)
	}
	s.Phase = "ready"
	if e = saveState(s); e != nil {
		return e
	}
	return copyFile(filepath.Join(s.Root, "release/jingjiaagent"), filepath.Join(s.Root, "jingjiaagent"), 0755)
}
