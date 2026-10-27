package main

import (
	"context"
	"crypto/rand"
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
	"syscall"
	"time"
)

// Resolution is shared by preflight and doctor. Random labels verify wildcard
// DNS rather than accepting a single manually registered preview hostname.
var lookupPreviewHost = net.DefaultResolver.LookupHost

type previewEndpoint struct {
	url       *url.URL
	addresses []string
}

func previewEndpoints(s *deployment) ([]previewEndpoint, error) {
	if s.PreviewMode == "off" || s.PreviewURL == "" {
		return nil, nil
	}
	base, err := url.Parse(s.PreviewURL)
	if err != nil || base.Hostname() == "" {
		return nil, errors.New("无效的预览域名")
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return nil, err
	}
	id[6], id[8] = (id[6]&0x0f)|0x40, (id[8]&0x3f)|0x80
	label := fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
	child := *base
	child.Host = label + "." + base.Host
	result := make([]previewEndpoint, 0, 2)
	for _, endpoint := range []*url.URL{base, &child} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		addresses, err := lookupPreviewHost(ctx, endpoint.Hostname())
		cancel()
		if err != nil || len(addresses) == 0 {
			return nil, fmt.Errorf("预览 DNS 未就绪：%s，请配置基域和通配子域指向网关: %v", endpoint.Hostname(), err)
		}
		for _, address := range addresses {
			if net.ParseIP(address) == nil {
				return nil, fmt.Errorf("预览 DNS 返回无效地址: %s", endpoint.Hostname())
			}
		}
		result = append(result, previewEndpoint{endpoint, addresses})
	}
	return result, nil
}

func checkPreviewDNS(s *deployment) error {
	_, err := previewEndpoints(s)
	return err
}

func probePreview(s *deployment, roots *x509.CertPool) error {
	endpoints, err := previewEndpoints(s)
	if err != nil {
		return err
	}
	for _, endpoint := range endpoints {
		// Probe all advertised addresses, preserving the domain in Host and TLS
		// SNI. This catches stale A/AAAA records instead of testing loopback only.
		for _, address := range endpoint.addresses {
			transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, net.JoinHostPort(address, originPort(endpoint.url)))
			}}
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			resp, err := client.Get(endpoint.url.String() + "/.well-known/jingjiaagent-preview-health")
			if err != nil {
				transport.CloseIdleConnections()
				return fmt.Errorf("预览 %s (%s): %w", endpoint.url.Hostname(), address, err)
			}
			var body struct {
				Service string `json:"service"`
			}
			err = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body)
			resp.Body.Close()
			transport.CloseIdleConnections()
			if err != nil || resp.StatusCode != http.StatusOK || body.Service != "jingjiaagent-preview" {
				return fmt.Errorf("预览 %s (%s) 不是可用的预览网关", endpoint.url.Hostname(), address)
			}
		}
	}
	if len(endpoints) > 0 {
		fmt.Println("预览基域及随机子域 DNS、HTTP/TLS 和代理链路正常；员工端网络与证书信任仍需从员工电脑验证。")
	}
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
		if !o.previewModeSet {
			next.PreviewMode = ""
		}
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
	s, err = readStateRecord(s.Root)
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
	if err = checkPreviewDNS(next); err != nil {
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
	if err = preparePreviewConfiguration(s, next, o, stage); err != nil {
		return err
	}
	backup, err := os.MkdirTemp(s.Root, "preview-before-configuration-*")
	if err != nil {
		return err
	}
	if err = copyManagedConfiguration(s, backup); err != nil {
		return err
	}
	// Block new task submissions before the second task-ledger check. Guests
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

func preparePreviewConfiguration(s, next *deployment, o options, stage string) error {
	if err := copyManagedConfiguration(s, stage); err != nil {
		return err
	}
	staged := *next
	staged.Root = stage
	if err := copyPreviewTLS(&staged, o); err != nil {
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
		if err := privateWrite(path, append(append(bundle, '\n'), ca...)); err != nil {
			return err
		}
	}
	if err := validateTLSSet(&staged); err != nil {
		certs, renewErr := renewedCertificates(&staged)
		if renewErr != nil {
			return fmt.Errorf("新预览地址不受当前证书覆盖；请提供 preview-tls-cert/key 和必要的 ca-cert: %w", renewErr)
		}
		for name, cert := range certs {
			if err := privateWrite(filepath.Join(stage, "tls", name+".crt"), cert); err != nil {
				return err
			}
		}
	}
	if err := render(&staged); err != nil {
		return err
	}
	if err := compose(&staged, "config", "-q"); err != nil {
		return err
	}
	if err := checkStagedGateway(&staged); err != nil {
		return err
	}
	return nil
}
