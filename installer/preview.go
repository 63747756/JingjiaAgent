package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const defaultPreviewRange = "31000-31099"

func parseTrustedProxies(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	result := []string{}
	seen := map[string]bool{}
	for _, item := range strings.Split(raw, ",") {
		p, err := netip.ParsePrefix(strings.TrimSpace(item))
		if err != nil || p.Bits() == 0 {
			return nil, errors.New("trusted-proxy-cidrs 必须为明确的代理 CIDR，不能信任全部来源")
		}
		value := p.Masked().String()
		if !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
	}
	return result, nil
}

func previewRange(raw string) (int, int, error) {
	parts := strings.Split(raw, "-")
	if len(parts) != 2 {
		return 0, 0, errors.New("preview-port-range 必须为 START-END")
	}
	start, e1 := strconv.Atoi(parts[0])
	end, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil || start < 1024 || end > 65535 || start > end || end-start >= 1000 || strconv.Itoa(start) != parts[0] || strconv.Itoa(end) != parts[1] {
		return 0, 0, errors.New("预览端口须在 1024–65535，范围不超过 1000 个端口")
	}
	return start, end, nil
}

func (s *deployment) normalizePreview() error {
	// Old installations remain disabled or keep their explicitly configured domain.
	// Fresh installs set port mode before this normalization step.
	if s.PreviewMode == "" {
		if s.PreviewURL == "" {
			s.PreviewMode = "off"
		} else {
			s.PreviewMode = "domain"
		}
	}
	if s.PreviewMode == "off" {
		s.PreviewURL, s.PreviewPortRange, s.PreviewSeparateTLS = "", "", false
		return nil
	}
	if s.PreviewMode != "port" && s.PreviewMode != "domain" {
		return errors.New("preview-mode 必须为 port、domain 或 off")
	}
	p, err := deploymentOrigin(s.PublicURL, true)
	if err != nil {
		return err
	}
	i, err := deploymentOrigin(s.internalURL(), false)
	if err != nil {
		return err
	}
	if s.PreviewURL == "" {
		if s.PreviewMode != "port" || net.ParseIP(p.Hostname()) == nil {
			return errors.New("请通过 --preview-url 指定员工可达的预览 IP 地址，域名模式须指定独立预览域名")
		}
		host := p.Hostname()
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		s.PreviewURL = p.Scheme + "://" + host
	}
	v, err := deploymentOrigin(s.PreviewURL, true)
	if err != nil {
		return fmt.Errorf("预览地址无效: %w", err)
	}
	if v.Path != "" {
		return errors.New("预览基础地址不能包含路径或末尾斜线")
	}
	if s.PreviewMode == "port" {
		if net.ParseIP(v.Hostname()) == nil || v.Port() != "" {
			return errors.New("端口模式 --preview-url 必须使用 IP，不能包含端口；域名请使用 --preview-mode domain")
		}
		if s.PreviewPortRange == "" {
			s.PreviewPortRange = defaultPreviewRange
		}
		start, end, err := previewRange(s.PreviewPortRange)
		if err != nil {
			return err
		}
		for _, origin := range []*url.URL{p, i} {
			port, _ := strconv.Atoi(originPort(origin))
			if port >= start && port <= end {
				return errors.New("预览端口池不能包含员工入口或内部节点监听端口")
			}
		}
	} else {
		if net.ParseIP(v.Hostname()) != nil || !strings.Contains(v.Hostname(), ".") || strings.Contains(v.Hostname(), "*") {
			return errors.New("域名模式需要独立的预览基础域名，不填写通配星号或 IP")
		}
		for _, origin := range []*url.URL{p, i} {
			if strings.EqualFold(origin.Hostname(), v.Hostname()) || strings.HasSuffix(strings.ToLower(origin.Hostname()), "."+strings.ToLower(v.Hostname())) {
				return errors.New("预览域名不能覆盖员工入口或内部节点域名")
			}
			if originPort(origin) == originPort(v) && origin.Scheme != v.Scheme {
				return errors.New("HTTP 与 HTTPS 不能共用监听端口")
			}
		}
		s.PreviewPortRange = ""
	}
	if s.PreviewSeparateTLS && v.Scheme != "https" {
		return errors.New("HTTP 预览不使用预览 TLS 证书")
	}
	return nil
}

func (s *deployment) previewPorts() []string {
	if s.PreviewMode == "off" || s.PreviewURL == "" {
		return nil
	}
	if s.PreviewMode == "domain" {
		u, _ := url.Parse(s.PreviewURL)
		return []string{originPort(u)}
	}
	start, end, _ := previewRange(s.PreviewPortRange)
	ports := make([]string, 0, end-start+1)
	for p := start; p <= end; p++ {
		ports = append(ports, strconv.Itoa(p))
	}
	return ports
}

func previewNetworkAddresses(raw string) (gateway, backend string, err error) {
	p, err := netip.ParsePrefix(raw)
	if err != nil || !p.Addr().Is4() || p.Bits() != 29 || p != p.Masked() || !p.Addr().IsPrivate() {
		return "", "", errors.New("预览入口网络必须为独立的私有 IPv4 /29 子网")
	}
	return p.Addr().Next().Next().String(), p.Addr().Next().Next().Next().String(), nil
}

// Select once and persist. Docker checks the allocation again when creating the
// network; never make every RFC1918 address a trusted reverse proxy.
func ensurePreviewNetwork(s *deployment) error {
	if s.PreviewSubnet != "" {
		_, _, err := previewNetworkAddresses(s.PreviewSubnet)
		return err
	}
	raw, err := output("docker", "network", "ls", "-q")
	if err != nil {
		return err
	}
	used := []netip.Prefix{}
	if ids := strings.Fields(string(raw)); len(ids) > 0 {
		raw, err = output("docker", append([]string{"network", "inspect"}, ids...)...)
		if err != nil {
			return err
		}
		var nets []struct {
			IPAM struct{ Config []struct{ Subnet string } }
		}
		if err = json.Unmarshal(raw, &nets); err != nil {
			return err
		}
		for _, n := range nets {
			for _, cfg := range n.IPAM.Config {
				if p, err := netip.ParsePrefix(cfg.Subnet); err == nil {
					used = append(used, p)
				}
			}
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range addrs {
			if p, err := netip.ParsePrefix(addr.String()); err == nil {
				used = append(used, p.Masked())
			}
		}
	}
	for second := 30; second >= 16; second-- {
		for third := 255; third >= 0; third-- {
			for fourth := 0; fourth < 256; fourth += 8 {
				candidate := netip.MustParsePrefix(fmt.Sprintf("172.%d.%d.%d/29", second, third, fourth))
				free := true
				for _, existing := range used {
					if candidate.Overlaps(existing) {
						free = false
						break
					}
				}
				if free {
					s.PreviewSubnet = candidate.String()
					return nil
				}
			}
		}
	}
	return errors.New("无法为预览入口分配独立网络：私有网络已被占用，请调整 Docker 网络规划")
}

func validatePreviewTLS(s *deployment, cert, key string) error {
	if !strings.HasPrefix(s.PreviewURL, "https://") {
		return nil
	}
	if err := validateTLS(s.PreviewURL, cert, key); err != nil {
		return err
	}
	if s.PreviewMode == "domain" {
		u, _ := url.Parse(s.PreviewURL)
		u.Host = net.JoinHostPort("preview-certificate-check."+u.Hostname(), originPort(u))
		return validateTLS(u.String(), cert, key)
	}
	return nil
}

func copyPreviewTLS(s *deployment, o options) error {
	if (o.PreviewCertificate == "") != (o.PreviewKey == "") {
		return errors.New("preview-tls-cert 与 preview-tls-key 必须同时提供")
	}
	if o.PreviewCertificate == "" {
		return nil
	}
	if !s.PreviewSeparateTLS {
		return errors.New("预览证书只能用于启用 HTTPS 预览的部署")
	}
	if err := validatePreviewTLS(s, o.PreviewCertificate, o.PreviewKey); err != nil {
		return fmt.Errorf("预览证书无效: %w", err)
	}
	for name, src := range map[string]string{"preview.crt": o.PreviewCertificate, "preview.key": o.PreviewKey} {
		if err := copyFile(src, filepath.Join(s.Root, "tls", name), 0600); err != nil {
			return err
		}
	}
	return nil
}

func previewGatewayServer(s *deployment) string {
	if s.PreviewMode == "off" || s.PreviewURL == "" {
		return ""
	}
	u, _ := url.Parse(s.PreviewURL)
	text := "\nserver {\n"
	for _, port := range s.previewPorts() {
		text += " listen " + port
		if u.Scheme == "https" {
			text += " ssl"
		}
		text += ";\n"
	}
	if s.PreviewMode == "domain" {
		text += " server_name ." + u.Hostname() + ";\n"
	} else {
		text += " server_name _;\n"
	}
	if u.Scheme == "https" {
		name := "server"
		if s.PreviewSeparateTLS {
			name = "preview"
		}
		text += " ssl_certificate /run/tls/" + name + ".crt; ssl_certificate_key /run/tls/" + name + ".key; ssl_protocols TLSv1.2 TLSv1.3;\n"
	}
	gateway, _, _ := previewNetworkAddresses(s.PreviewSubnet)
	text += " client_max_body_size 128m;\n location / {\n  proxy_pass http://backend-preview:8889; proxy_bind " + gateway + ";\n  proxy_set_header Host $http_host;\n  proxy_set_header X-Forwarded-Proto $scheme;\n  proxy_set_header X-Forwarded-For $remote_addr;\n  proxy_set_header X-Real-IP $remote_addr;\n  proxy_set_header Forwarded \"\";\n  proxy_set_header X-JingjiaAgent-Preview-Port $server_port;\n  proxy_http_version 1.1; proxy_set_header Upgrade $http_upgrade; proxy_set_header Connection $connection_upgrade;\n  proxy_buffering off; proxy_read_timeout 900s;\n }\n}\n"
	return text
}

func reportPreview(s *deployment) {
	if s.PreviewMode == "off" {
		fmt.Println("预览已禁用。")
		return
	}
	if s.PreviewMode == "port" {
		fmt.Printf("预览：%s，需允许员工网络访问 TCP %s；每个映射使用一个外部端口。\n", s.PreviewURL, s.PreviewPortRange)
	} else {
		u, _ := url.Parse(s.PreviewURL)
		fmt.Printf("预览：%s，配置 *.%s 的 DNS 并放行 TCP %s。\n", s.PreviewURL, u.Hostname(), originPort(u))
	}
	if strings.HasPrefix(s.PreviewURL, "https://") {
		fmt.Println("HTTPS 预览需要员工客户端信任对应证书；安装器不会自动为员工电脑安装 CA。")
	}
	fmt.Println("预览免登录，仍需 IP 白名单。安装器不修改主机防火墙。")
}

// Copy only regular installer-owned files into a private staging directory.
func copyManagedConfiguration(s *deployment, stage string) error {
	for _, name := range []string{"deployment.json", "compose.json", "gateway.conf", "web.conf", "runtime-proxy.conf", "config/server/config.yaml"} {
		if err := copyFile(filepath.Join(s.Root, name), filepath.Join(stage, name), 0600); err != nil {
			return err
		}
	}
	for _, dir := range []string{"tls", "secrets"} {
		entries, err := os.ReadDir(filepath.Join(s.Root, dir))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() {
				return errors.New("拒绝包含非普通文件的配置目录")
			}
			if err := copyFile(filepath.Join(s.Root, dir, entry.Name()), filepath.Join(stage, dir, entry.Name()), 0600); err != nil {
				return err
			}
		}
	}
	return nil
}
