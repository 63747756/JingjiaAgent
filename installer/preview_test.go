package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func previewTestDeployment(t *testing.T) *deployment {
	t.Helper()
	return &deployment{Schema: 1, Root: t.TempDir(), Data: t.TempDir(), PublicURL: "https://192.0.2.10", PreviewMode: "port", PreviewSubnet: "172.30.240.0/29", Project: "preview-test", Mode: "controller", Phase: "preparing", Images: map[string]imageRecord{"nginx": {ID: "sha256:" + strings.Repeat("a", 64)}}}
}

func TestPreviewAddressMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, mode, address, public, internal, pool string
		valid                                       bool
	}{
		{"default", "port", "", "http://192.0.2.10", "", "", true},
		{"http-ip", "port", "http://192.0.2.10", "https://console.example.test", "", "31000-31009", true},
		{"https-ip", "port", "https://192.0.2.10", "http://console.example.test", "", "", true},
		{"http-domain", "domain", "http://preview.example.test", "https://console.example.test", "", "", true},
		{"https-domain", "domain", "https://preview.example.test", "https://console.example.test", "", "", true},
		{"custom-domain-port", "domain", "http://preview.example.test:8081", "https://console.example.test", "", "", true},
		{"same-http-port-different-host", "domain", "http://preview.example.test", "http://console.example.test", "", "", true},
		{"ipv6", "port", "http://[fd00::10]", "https://[fd00::10]", "", "", true},
		{"port-on-ip", "port", "http://192.0.2.10:31000", "https://192.0.2.10", "", "", false},
		{"domain-in-port-mode", "port", "http://preview.example.test", "https://192.0.2.10", "", "", false},
		{"ip-in-domain-mode", "domain", "http://192.0.2.10", "https://192.0.2.10", "", "", false},
		{"ambiguous-console-host", "port", "", "https://console.example.test", "", "", false},
		{"scheme-collision", "domain", "http://preview.example.test:443", "https://console.example.test", "", "", false},
		{"same-console-domain", "domain", "http://console.example.test", "https://console.example.test", "", "", false},
		{"covers-console-domain", "domain", "http://example.test", "https://console.example.test", "", "", false},
		{"control-port-overlap", "port", "http://192.0.2.10", "https://192.0.2.10:31001", "", "31000-31002", false},
		{"too-many-ports", "port", "http://192.0.2.10", "https://192.0.2.10", "", "31000-32000", false},
		{"low-ports", "port", "http://192.0.2.10", "https://192.0.2.10", "", "80-90", false},
		{"path", "domain", "http://preview.example.test/x", "https://192.0.2.10", "", "", false},
		{"off", "off", "", "https://console.example.test", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := deployment{PublicURL: tc.public, InternalURL: tc.internal, PreviewMode: tc.mode, PreviewURL: tc.address, PreviewPortRange: tc.pool}
			err := s.normalizeAddresses()
			if (err == nil) != tc.valid {
				t.Fatalf("normalize error=%v", err)
			}
			if err == nil && tc.name == "default" && (s.PreviewURL != "http://192.0.2.10" || s.PreviewPortRange != defaultPreviewRange) {
				t.Fatal("wrong inherited defaults", s.PreviewURL, s.PreviewPortRange)
			}
		})
	}
}

func TestPreviewRenderPublishesOnlyGatewayAndPreservesProviderSettings(t *testing.T) {
	s := previewTestDeployment(t)
	s.PreviewURL = "http://192.0.2.10"
	s.PreviewPortRange = "31000-31002"
	if err := prepareTLS(s, options{}); err != nil {
		t.Fatal(err)
	}
	if err := render(s); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, "config/server/config.yaml")
	raw, _ := os.ReadFile(path)
	var cfg object
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["work"].(object)["skillhub"] = object{"url": "https://hub.example.test"}
	cfg["runtime"].(object)["preview"] = object{"base_url": "https://old.example.test", "trusted_proxies": []string{"10.0.0.0/8"}}
	if err := writeJSON(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := render(s); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	preview := cfg["runtime"].(object)["preview"].(object)
	if preview["mode"] != "port" || preview["base_url"] != s.PreviewURL || preview["port_range"] != "31000-31002" || preview["trusted_proxies"].([]any)[0] != "172.30.240.2/32" {
		t.Fatal("wrong managed preview config", preview)
	}
	if cfg["work"].(object)["skillhub"].(object)["url"] != "https://hub.example.test" {
		t.Fatal("integration lost")
	}
	raw, _ = os.ReadFile(filepath.Join(s.Root, "compose.json"))
	var composition object
	json.Unmarshal(raw, &composition)
	services := composition["services"].(object)
	if _, ok := services["backend"].(object)["ports"]; ok {
		t.Fatal("private backend published")
	}
	ports := services["gateway"].(object)["ports"].([]any)
	if len(ports) != 4 {
		t.Fatal("unexpected published ports", ports)
	}
	for _, port := range []string{"31000:31000", "31001:31001", "31002:31002"} {
		if !strings.Contains(string(raw), port) {
			t.Fatal("missing port", port)
		}
	}
	if composition["networks"].(object)["preview_ingress"].(object)["internal"] != true {
		t.Fatal("preview ingress not isolated")
	}
	raw, _ = os.ReadFile(filepath.Join(s.Root, "gateway.conf"))
	text := string(raw)
	for _, expected := range []string{"proxy_pass http://backend-preview:8889; proxy_bind 172.30.240.2", "proxy_pass http://backend-preview:8888; proxy_bind 172.30.240.2", "proxy_set_header X-Forwarded-For $remote_addr", "proxy_set_header X-JingjiaAgent-Preview-Port $server_port", "listen 31002;"} {
		if !strings.Contains(text, expected) {
			t.Fatal("missing gateway contract", expected)
		}
	}
	if strings.Contains(text, "listen 31000 ssl") || strings.Contains(text, "$proxy_add_x_forwarded_for") {
		t.Fatal("preview scheme or client IP can be spoofed")
	}
}

func TestPreviewDomainRenderingAndTLSCoverage(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			s := previewTestDeployment(t)
			s.PreviewMode = "domain"
			s.PreviewURL = scheme + "://preview.example.test:9444"
			if err := prepareTLS(s, options{}); err != nil {
				t.Fatal(err)
			}
			if err := render(s); err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(filepath.Join(s.Root, "gateway.conf"))
			text := string(raw)
			listen := "listen 9444"
			if scheme == "https" {
				listen += " ssl"
			}
			if !strings.Contains(text, listen+";") || !strings.Contains(text, "server_name .preview.example.test;") {
				t.Fatal("wrong domain listener")
			}
			certPEM, _ := os.ReadFile(filepath.Join(s.Root, "tls/server.crt"))
			block, _ := pem.Decode(certPEM)
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			wildcard := cert.VerifyHostname("a7f3.preview.example.test") == nil
			if wildcard != (scheme == "https") {
				t.Fatalf("certificate wildcard=%v for %s", wildcard, scheme)
			}
		})
	}
}

func TestPreviewOptionsPreserveUnspecifiedSettings(t *testing.T) {
	s := previewTestDeployment(t)
	s.PreviewURL = "http://192.0.2.10"
	s.PreviewPortRange = "32000-32010"
	s.TrustedProxyCIDRs = []string{"192.0.2.1/32"}
	next, err := previewOptions(s, options{PreviewMode: "port", PreviewPortRange: defaultPreviewRange})
	if err != nil {
		t.Fatal(err)
	}
	if next.PreviewPortRange != s.PreviewPortRange || next.PreviewURL != s.PreviewURL || next.PublicURL != s.PublicURL || len(next.TrustedProxyCIDRs) != 1 {
		t.Fatal("defaults overwrote saved state")
	}
	next, err = previewOptions(s, options{PreviewMode: "off", previewModeSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if next.PreviewURL != "" || len(next.previewPorts()) != 0 {
		t.Fatal("off still publishes previews")
	}
	if err = validatePreviewShrink(s, []int{32001, 32010}); err != nil {
		t.Fatal(err)
	}
	if err = validatePreviewShrink(s, []int{31000}); err == nil {
		t.Fatal("occupied port silently removed")
	}
	if s.PreviewURL != "http://192.0.2.10" {
		t.Fatal("changed input state")
	}
	secure := *s
	secure.PreviewURL = "https://192.0.2.10"
	secure.PreviewSeparateTLS = true
	next, err = previewOptions(&secure, options{PreviewURL: "http://192.0.2.10", previewURLSet: true})
	if err != nil || next.PreviewSeparateTLS {
		t.Fatal("cannot switch independent HTTPS preview to HTTP", err)
	}
}

func TestSeparatePreviewCertificateKeepsMainIdentity(t *testing.T) {
	issuer := previewTestDeployment(t)
	issuer.PublicURL = "https://192.0.2.11"
	issuer.PreviewMode = "off"
	if err := prepareTLS(issuer, options{}); err != nil {
		t.Fatal(err)
	}
	s := previewTestDeployment(t)
	s.PreviewURL = "https://192.0.2.11"
	s.PreviewSeparateTLS = true
	o := options{PreviewCertificate: filepath.Join(issuer.Root, "tls/server.crt"), PreviewKey: filepath.Join(issuer.Root, "tls/server.key"), CA: filepath.Join(issuer.Root, "tls/ca.crt")}
	if err := prepareTLS(s, o); err != nil {
		t.Fatal(err)
	}
	if err := validateTLSSet(s); err != nil {
		t.Fatal(err)
	}
	if validateTLS(s.PreviewURL, filepath.Join(s.Root, "tls/server.crt"), filepath.Join(s.Root, "tls/server.key")) == nil {
		t.Fatal("independent preview identity leaked into main certificate")
	}
	if err := validatePreviewTLS(s, filepath.Join(s.Root, "tls/preview.crt"), filepath.Join(s.Root, "tls/preview.key")); err != nil {
		t.Fatal(err)
	}
	if err := render(s); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(s.Root, "gateway.conf"))
	if !strings.Contains(string(raw), "ssl_certificate /run/tls/preview.crt") {
		t.Fatal("independent preview certificate not served")
	}
	if _, err := renewedCertificates(s); err != nil {
		t.Fatal("main TLS renewal lost independent preview certificate", err)
	}
	bad := previewTestDeployment(t)
	bad.PreviewURL = "https://192.0.2.12"
	bad.PreviewSeparateTLS = true
	if prepareTLS(bad, o) == nil {
		t.Fatal("certificate for unrelated preview address accepted")
	}
}

func TestPreviewGatewayProbeChecksServiceAndTLS(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/jingjiaagent-preview-health" {
			t.Error("wrong health path")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"service":"jingjiaagent-preview"}`))
	})
	plain := httptest.NewServer(handler)
	defer plain.Close()
	secure := httptest.NewTLSServer(handler)
	defer secure.Close()
	for _, server := range []*httptest.Server{plain, secure} {
		u, _ := url.Parse(server.URL)
		pool := x509.NewCertPool()
		if server.TLS != nil {
			pool.AddCert(server.Certificate())
		}
		s := &deployment{PreviewMode: "port", PreviewURL: u.Scheme + "://" + u.Hostname(), PreviewPortRange: u.Port() + "-" + u.Port()}
		if err := probePreview(s, pool); err != nil {
			t.Fatal(err)
		}
		if server.TLS != nil && probePreview(s, x509.NewCertPool()) == nil {
			t.Fatal("untrusted TLS accepted")
		}
	}
	wrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"service":"other"}`)) }))
	defer wrong.Close()
	u, _ := url.Parse(wrong.URL)
	if probePreview(&deployment{PreviewMode: "port", PreviewURL: "http://" + u.Hostname(), PreviewPortRange: u.Port() + "-" + u.Port()}, nil) == nil {
		t.Fatal("non-preview application accepted")
	}
}

func TestPreviewPortConflictAndExplicitProxyTrust(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	// The installer runs on Linux. Windows permits a wildcard socket alongside
	// some address-specific listeners and does not provide the same bind check.
	if runtime.GOOS == "linux" && checkPreviewPortAvailability(&deployment{}, &deployment{PreviewMode: "port", PreviewURL: "http://192.0.2.10", PreviewPortRange: port + "-" + port}) == nil {
		t.Fatal("occupied port accepted")
	}
	for _, raw := range []string{"0.0.0.0/0", "::/0", "10.0.0.0/8; include bad", "example.test"} {
		if _, err := parseTrustedProxies(raw); err == nil {
			t.Fatal("invalid proxy accepted", raw)
		}
	}
	proxies, err := parseTrustedProxies("192.0.2.7/32, 2001:db8::/64,192.0.2.7/32")
	if err != nil || len(proxies) != 2 {
		t.Fatal("explicit proxies", proxies, err)
	}
	if _, _, err := previewNetworkAddresses("172.30.240.0/16"); err == nil {
		t.Fatal("broad trusted network accepted")
	}
}
