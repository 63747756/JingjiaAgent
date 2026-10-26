package main

import (
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
	"strconv"
	"strings"
	"syscall"
	"time"
)

func probePreview(s *deployment, roots *x509.CertPool) error {
	if s.PreviewMode == "off" || s.PreviewURL == "" {
		return nil
	}
	// Every published port is checked, not only the first port in the pool.
	for _, port := range s.previewPorts() {
		u, _ := url.Parse(s.PreviewURL)
		if s.PreviewMode == "port" {
			u.Host = net.JoinHostPort(u.Hostname(), port)
		}
		transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
		}}
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Get(u.String() + "/.well-known/jingjiaagent-preview-health")
		if err != nil {
			transport.CloseIdleConnections()
			return fmt.Errorf("端口 %s: %w", port, err)
		}
		var body struct {
			Service string `json:"service"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body)
		resp.Body.Close()
		transport.CloseIdleConnections()
		if err != nil || resp.StatusCode != http.StatusOK || body.Service != "jingjiaagent-preview" {
			return fmt.Errorf("端口 %s 不是可用的预览网关", port)
		}
	}
	fmt.Println("预览本机入口、协议和代理链路正常；员工网络、防火墙及通配 DNS 仍须从员工端验证。")
	return nil
}

func previewOptions(s *deployment, o options) (*deployment, error) {
	next := *s
	if o.trustedProxySet {
		proxies, err := parseTrustedProxies(o.TrustedProxyCIDRs)
		if err != nil {
			return nil, err
		}
		next.TrustedProxyCIDRs = proxies
	}
	if o.previewModeSet {
		next.PreviewMode = o.PreviewMode
	}
	if o.previewURLSet {
		next.PreviewURL = o.PreviewURL
	}
	if o.previewRangeSet {
		next.PreviewPortRange = o.PreviewPortRange
	}
	if o.previewModeSet && o.PreviewMode != s.PreviewMode && !o.previewURLSet {
		next.PreviewURL = ""
	}
	if o.PreviewCertificate != "" {
		next.PreviewSeparateTLS = true
	}
	if strings.HasPrefix(next.PreviewURL, "http://") && o.PreviewCertificate == "" {
		next.PreviewSeparateTLS = false
	}
	if (o.PreviewCertificate == "") != (o.PreviewKey == "") {
		return nil, errors.New("preview-tls-cert 与 preview-tls-key 必须同时提供")
	}
	if err := next.normalizeAddresses(); err != nil {
		return nil, err
	}
	return &next, nil
}

func mappedPreviewPorts(s *deployment) ([]int, error) {
	// Retained and cooling-down mappings count too; a range shrink must not hide
	// an existing task URL or silently reassign a reserved port.
	raw, err := output("docker", composeArgs(s, "exec", "-T", "postgres", "psql", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "jingjiaagent", "-Atc", "SELECT DISTINCT public_port FROM runtime_port_forwards WHERE public_port IS NOT NULL AND public_port > 0 AND (released_at IS NULL OR released_at >= now()-interval '24 hours') ORDER BY public_port")...)
	if err != nil {
		return nil, errors.New("无法确认已占用的预览端口，请先升级并启动后台完成数据库迁移")
	}
	ports := []int{}
	for _, line := range strings.Fields(string(raw)) {
		p, err := strconv.Atoi(line)
		if err != nil || p < 1 || p > 65535 {
			return nil, errors.New("无效的预览端口记录")
		}
		ports = append(ports, p)
	}
	return ports, nil
}

func validatePreviewShrink(next *deployment, ports []int) error {
	if next.PreviewMode != "port" {
		return nil
	}
	start, end, err := previewRange(next.PreviewPortRange)
	if err != nil {
		return err
	}
	for _, port := range ports {
		if port < start || port > end {
			return fmt.Errorf("端口 %d 仍有预览映射，不能缩小端口池；请先关闭并删除对应任务映射", port)
		}
	}
	return nil
}

func checkPreviewPortAvailability(old, next *deployment) error {
	owned := map[string]bool{}
	for _, port := range old.previewPorts() {
		owned[port] = true
	}
	for _, address := range []string{old.PublicURL, old.internalURL()} {
		if address == "" {
			continue
		}
		u, _ := url.Parse(address)
		owned[originPort(u)] = true
	}
	listeners := []net.Listener{}
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()
	for _, port := range next.previewPorts() {
		if owned[port] {
			continue
		}
		for _, family := range []struct{ network, host string }{{"tcp4", "0.0.0.0"}, {"tcp6", "::"}} {
			l, err := net.Listen(family.network, net.JoinHostPort(family.host, port))
			if err != nil {
				if family.network == "tcp6" && errors.Is(err, syscall.EAFNOSUPPORT) {
					continue
				}
				return fmt.Errorf("预览端口 %s 已被占用或无法监听: %w", port, err)
			}
			listeners = append(listeners, l)
		}
	}
	return nil
}

func configurePreview(s *deployment, o options) (result error) {
	if err := requireLinux(); err != nil {
		return err
	}
	unlock, err := lock(s.Root)
	if err != nil {
		return err
	}
	defer unlock()
	s, err = readState(s.Root)
	if err != nil {
		return err
	}
	if s.Phase != "ready" {
		return errors.New("请先完成已有安装或恢复操作，再重配置预览")
	}
	next, err := previewOptions(s, o)
	if err != nil {
		return err
	}
	if err = ensurePreviewNetwork(next); err != nil {
		return err
	}
	if err = checkPreviewPortAvailability(s, next); err != nil {
		return err
	}
	ports, err := mappedPreviewPorts(s)
	if err != nil {
		return err
	}
	if err = validatePreviewShrink(next, ports); err != nil {
		return err
	}
	if err = activeCommands(s); err != nil {
		return err
	}
	if err = checkSecretFiles(s); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(s.Root, ".preview-config-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage) // Installer-created staging directory only.
	if err = copyManagedConfiguration(s, stage); err != nil {
		return err
	}
	staged := *next
	staged.Root = stage
	if err = copyPreviewTLS(&staged, o); err != nil {
		return err
	}
	if o.CA != "" {
		ca, err := os.ReadFile(o.CA)
		if err != nil {
			return err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			return errors.New("无效的 CA 文件")
		}
		path := filepath.Join(stage, "tls/ca-bundle.crt")
		bundle, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err = privateWrite(path, append(append(bundle, '\n'), ca...)); err != nil {
			return err
		}
	}
	if err = validateTLSSet(&staged); err != nil {
		certs, renewErr := renewedCertificates(&staged)
		if renewErr != nil {
			return fmt.Errorf("新预览地址不受当前证书覆盖；请提供 preview-tls-cert/key 和必要的 ca-cert: %w", renewErr)
		}
		for name, cert := range certs {
			if err = privateWrite(filepath.Join(stage, "tls", name+".crt"), cert); err != nil {
				return err
			}
		}
	}
	if err = render(&staged); err != nil {
		return err
	}
	if err = compose(&staged, "config", "-q"); err != nil {
		return err
	}
	if err = checkStagedGateway(&staged); err != nil {
		return err
	}
	backup, err := os.MkdirTemp(s.Root, "preview-before-configuration-*")
	if err != nil {
		return err
	}
	if err = copyManagedConfiguration(s, backup); err != nil {
		return err
	}
	// Block new task submissions before the second ledger/mapping check. Guests
	// remain running; only the API and public gateway are briefly restarted.
	changed := false
	defer func() {
		if result == nil {
			return
		}
		if changed {
			if err := restorePreviewFiles(backup, s.Root); err != nil {
				result = fmt.Errorf("%w；回滚配置失败: %v，备份 %s", result, err, backup)
				return
			}
		}
		if err := compose(s, "up", "-d", "--no-deps", "--force-recreate", "--wait", "--wait-timeout", "120", "backend", "gateway"); err != nil {
			result = fmt.Errorf("%w；恢复原服务失败: %v，备份 %s", result, err, backup)
		}
	}()
	if err = compose(s, "stop", "gateway", "backend"); err != nil {
		return err
	}
	if err = activeCommands(s); err != nil {
		return err
	}
	ports, err = mappedPreviewPorts(s)
	if err != nil {
		return err
	}
	if err = validatePreviewShrink(next, ports); err != nil {
		return err
	}
	changed = true
	if err = installPreviewFiles(stage, s.Root, stage); err != nil {
		return err
	}
	if err = saveState(next); err != nil {
		return err
	}
	if err = compose(next, "up", "-d", "--no-deps", "--force-recreate", "--wait", "--wait-timeout", "120", "backend", "gateway"); err != nil {
		return err
	}
	if err = doctor(next); err != nil {
		return err
	}
	fmt.Printf("预览配置已更新，原配置备份：%s。已有预览连接可能需要重新打开。\n", backup)
	return nil
}

func checkStagedGateway(s *deployment) error {
	args := []string{"run", "--rm", "--network", "none"}
	for _, name := range []string{"backend-preview", "backend", "web", "storage", "runtime"} {
		args = append(args, "--add-host", name+":127.0.0.1")
	}
	args = append(args, "--mount", "type=bind,src="+filepath.Join(s.Root, "gateway.conf")+",dst=/etc/nginx/conf.d/default.conf,readonly", "--mount", "type=bind,src="+filepath.Join(s.Root, "tls")+",dst=/run/tls,readonly", s.Images["nginx"].ID, "nginx", "-t")
	return execute("docker", args...)
}

func installPreviewFiles(from, to, replaceRoot string) error {
	for _, name := range []string{"deployment.json", "compose.json", "gateway.conf", "web.conf", "runtime-proxy.conf", "config/server/config.yaml"} {
		data, err := os.ReadFile(filepath.Join(from, name))
		if err != nil {
			return err
		}
		if name == "compose.json" {
			data = []byte(strings.ReplaceAll(string(data), filepath.ToSlash(replaceRoot), filepath.ToSlash(to)))
		}
		if err = privateWrite(filepath.Join(to, name), data); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(filepath.Join(from, "tls"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			return errors.New("invalid staged TLS file")
		}
		if err = copyFile(filepath.Join(from, "tls", entry.Name()), filepath.Join(to, "tls", entry.Name()), 0600); err != nil {
			return err
		}
	}
	return nil
}

func restorePreviewFiles(backup, root string) error {
	if err := installPreviewFiles(backup, root, root); err != nil {
		return err
	}
	// A failed first-time separate-certificate configuration creates files that
	// did not exist in the backup. Restore their absence as well as the contents
	// of existing files, without removing unrelated operator-managed TLS files.
	for _, name := range []string{"preview.crt", "preview.key"} {
		if _, err := os.Lstat(filepath.Join(backup, "tls", name)); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Remove(filepath.Join(root, "tls", name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
