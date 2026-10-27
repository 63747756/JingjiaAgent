package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

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

func (s *deployment) normalizePreview() error {
	if s.PreviewMode == "" {
		s.PreviewMode = "off"
		if s.PreviewURL != "" {
			s.PreviewMode = "domain"
		}
	}
	if s.PreviewMode == "off" {
		s.PreviewURL, s.PreviewSeparateTLS = "", false
		return nil
	}
	if s.PreviewMode == "port" {
		return errors.New("IP 端口预览已移除，请提供 --preview-url http(s)://独立预览域名，或显式使用 --preview-mode off；现有服务尚未停止")
	}
	if s.PreviewMode != "domain" {
		return errors.New("preview-mode 仅支持 domain 或 off")
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
		return errors.New("域名预览需要 --preview-url http(s)://独立预览域名")
	}
	v, err := deploymentOrigin(s.PreviewURL, true)
	if err != nil {
		return fmt.Errorf("预览地址无效: %w", err)
	}
	if v.Path != "" {
		return errors.New("预览基础地址不能包含路径或末尾斜线")
	}
	if net.ParseIP(v.Hostname()) != nil || !strings.Contains(v.Hostname(), ".") || strings.Contains(v.Hostname(), "*") {
		return errors.New("预览仅支持独立基础域名，不填写通配星号或 IP；也可使用 --preview-mode off 关闭预览")
	}
	for _, origin := range []*url.URL{p, i} {
		if strings.EqualFold(origin.Hostname(), v.Hostname()) || strings.HasSuffix(strings.ToLower(origin.Hostname()), "."+strings.ToLower(v.Hostname())) {
			return errors.New("预览域名不能覆盖员工入口或内部节点域名")
		}
		if originPort(origin) == originPort(v) && origin.Scheme != v.Scheme {
			return errors.New("HTTP 与 HTTPS 不能共用监听端口")
		}
	}
	if s.PreviewSeparateTLS && v.Scheme != "https" {
		return errors.New("HTTP 预览不使用预览 TLS 证书")
	}
	return nil
}

func (s *deployment) previewPorts() []string {
	if s.PreviewMode != "domain" || s.PreviewURL == "" {
		return nil
	}
	u, err := url.Parse(s.PreviewURL)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	return []string{originPort(u)}
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
	u, _ := url.Parse(s.PreviewURL)
	data, err := os.ReadFile(cert)
	if err != nil {
		return err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return errors.New("无效的预览证书")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	wildcard := "*." + u.Hostname()
	for _, name := range leaf.DNSNames {
		if strings.EqualFold(name, wildcard) {
			return nil
		}
	}
	return fmt.Errorf("预览证书缺少通配域名 %s", wildcard)
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
	text += " server_name ." + u.Hostname() + ";\n"
	if u.Scheme == "https" {
		name := "server"
		if s.PreviewSeparateTLS {
			name = "preview"
		}
		text += " ssl_certificate /run/tls/" + name + ".crt; ssl_certificate_key /run/tls/" + name + ".key; ssl_protocols TLSv1.2 TLSv1.3;\n"
	}
	gateway, _, _ := previewNetworkAddresses(s.PreviewSubnet)
	text += " client_max_body_size 128m;\n location / {\n  proxy_pass http://backend-preview:8889; proxy_bind " + gateway + ";\n  proxy_set_header Host $http_host;\n  proxy_set_header X-Forwarded-Proto $scheme;\n  proxy_set_header X-Forwarded-For $remote_addr;\n  proxy_set_header X-Real-IP $remote_addr;\n  proxy_set_header Forwarded \"\";\n  proxy_http_version 1.1; proxy_set_header Upgrade $http_upgrade; proxy_set_header Connection $connection_upgrade;\n  proxy_buffering off; proxy_read_timeout 900s;\n }\n}\n"
	return text
}

func reportPreview(s *deployment) {
	if s.PreviewMode == "off" {
		fmt.Println("预览已禁用。")
		return
	}
	u, _ := url.Parse(s.PreviewURL)
	fmt.Printf("预览：%s，配置 %s 与 *.%s 的 DNS 并放行 TCP %s。\n", s.PreviewURL, u.Hostname(), u.Hostname(), originPort(u))
	if strings.HasPrefix(s.PreviewURL, "https://") {
		fmt.Println("HTTPS 预览需要员工客户端信任对应证书；安装器不会自动为员工电脑安装 CA。")
	}
	fmt.Println("新预览默认公开、免平台登录，可在任务中限制 IP 或关闭访问。安装器不修改主机防火墙。")
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
