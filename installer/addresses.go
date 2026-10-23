package main

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// PublicURL is the employee-facing origin. InternalURL is reachable by nodes
// and workspaces and always uses TLS, including when the console uses HTTP.
func deploymentOrigin(raw string, allowHTTP bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && !(allowHTTP && u.Scheme == "http")) ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" ||
		(u.Path != "" && u.Path != "/") || strings.ContainsAny(u.Host, "$;{}\"' \\%\t\r\n") {
		return nil, errors.New("访问地址必须为 HTTP/HTTPS 主机地址，不含路径、账号、查询参数或片段；节点地址必须使用 HTTPS")
	}
	if strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("访问地址端口不能为空")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return nil, errors.New("访问地址端口必须在 1–65535 之间")
		}
	}
	return u, nil
}

func internalURL(public, internal string) string {
	if internal != "" {
		return strings.TrimRight(internal, "/")
	}
	u, err := url.Parse(public)
	if err != nil || u.Scheme != "http" {
		return strings.TrimRight(public, "/")
	}
	return "https://" + net.JoinHostPort(u.Hostname(), "8443")
}

func (s *deployment) internalURL() string { return internalURL(s.PublicURL, s.InternalURL) }

// Persist browser-compatible origins, including when reading older state.
// Validation runs first so normalization cannot hide invalid input.
func (s *deployment) normalizeAddresses() error {
	if err := validateAddresses(s.PublicURL, s.InternalURL, s.PreviewURL); err != nil {
		return err
	}
	canonical := func(raw string) string {
		u, _ := deploymentOrigin(raw, true)
		u.Path, u.RawPath = "", ""
		host := strings.ToLower(u.Hostname())
		if ip := net.ParseIP(host); ip != nil {
			host = ip.String()
		}
		port := u.Port()
		if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
			port = ""
		}
		u.Host = host
		if port != "" {
			u.Host = net.JoinHostPort(host, port)
		} else if strings.Contains(host, ":") {
			u.Host = "[" + host + "]"
		}
		return u.String()
	}
	s.InternalURL = canonical(s.internalURL())
	s.PublicURL = canonical(s.PublicURL)
	if s.PreviewURL != "" {
		s.PreviewURL = canonical(s.PreviewURL)
	}
	return nil
}

func originPort(u *url.URL) string {
	if u.Port() != "" {
		return u.Port()
	}
	if u.Scheme == "http" {
		return "80"
	}
	return "443"
}

func validateAddresses(public, internal, preview string) error {
	p, err := deploymentOrigin(public, true)
	if err != nil {
		return err
	}
	i, err := deploymentOrigin(internalURL(public, internal), false)
	if err != nil {
		return fmt.Errorf("内部节点地址无效: %w", err)
	}
	if p.Scheme == "http" && (originPort(p) == originPort(i) || (preview != "" && originPort(p) == "443")) {
		return errors.New("HTTP 与 HTTPS 不能共用监听端口，请使用不同的 --public-url 和 --internal-url 端口")
	}
	if preview != "" {
		v, err := deploymentOrigin(preview, false)
		if err != nil {
			return err
		}
		if v.Path != "" || v.Port() != "" || net.ParseIP(v.Hostname()) != nil {
			return errors.New("preview requires a separate wildcard domain without a path or port")
		}
		for _, origin := range []*url.URL{p, i} {
			if strings.EqualFold(origin.Hostname(), v.Hostname()) || strings.HasSuffix(strings.ToLower(origin.Hostname()), "."+strings.ToLower(v.Hostname())) {
				return errors.New("preview domain must be separate from console and internal node domains")
			}
		}
	}
	return nil
}

func (s *deployment) tlsOrigins() []string {
	addresses := []string{s.internalURL()}
	if strings.HasPrefix(s.PublicURL, "https://") && s.PublicURL != addresses[0] {
		addresses = append(addresses, s.PublicURL)
	}
	if s.PreviewURL != "" {
		addresses = append(addresses, s.PreviewURL)
	}
	return addresses
}

func setServerNames(cert *x509.Certificate, s *deployment) {
	seen := map[string]bool{}
	for _, address := range s.tlsOrigins() {
		u, _ := url.Parse(address)
		name := u.Hostname()
		if seen[name] {
			continue
		}
		seen[name] = true
		if ip := net.ParseIP(name); ip != nil {
			cert.IPAddresses = append(cert.IPAddresses, ip)
		} else {
			cert.DNSNames = append(cert.DNSNames, name)
		}
	}
	if s.PreviewURL != "" {
		u, _ := url.Parse(s.PreviewURL)
		cert.DNSNames = append(cert.DNSNames, "*."+u.Hostname())
	}
}

func validateServerTLS(s *deployment, cert, key string) error {
	for _, address := range s.tlsOrigins() {
		if err := validateTLS(address, cert, key); err != nil {
			return err
		}
	}
	return nil
}
