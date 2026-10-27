package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
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
	return &deployment{Schema: 1, Root: t.TempDir(), Data: t.TempDir(), PublicURL: "https://192.0.2.10", PreviewMode: "domain", PreviewSubnet: "172.30.240.0/29", Project: "preview-test", Mode: "controller", Phase: "preparing", Images: map[string]imageRecord{"nginx": {ID: "sha256:" + strings.Repeat("a", 64)}}}
}

func TestPreviewAddressMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, mode, address, public string
		valid                       bool
	}{
		{"default-off", "", "", "http://192.0.2.10", true},
		{"infer-domain", "", "http://preview.example.test", "https://console.example.test", true},
		{"http-domain", "domain", "http://preview.example.test", "https://console.example.test", true},
		{"https-domain", "domain", "https://preview.example.test", "https://console.example.test", true},
		{"custom-domain-port", "domain", "http://preview.example.test:8081", "https://console.example.test", true},
		{"shared-http-port", "domain", "http://preview.example.test", "http://console.example.test", true},
		{"old-port-mode", "port", "http://192.0.2.10", "https://console.example.test", false},
		{"ip-domain", "domain", "http://192.0.2.10", "https://console.example.test", false},
		{"ipv6-domain", "domain", "http://[fd00::10]", "https://console.example.test", false},
		{"missing-domain", "domain", "", "https://console.example.test", false},
		{"scheme-collision", "domain", "http://preview.example.test:443", "https://console.example.test", false},
		{"same-console", "domain", "http://console.example.test", "https://console.example.test", false},
		{"covers-console", "domain", "http://example.test", "https://console.example.test", false},
		{"wildcard-input", "domain", "http://*.preview.example.test", "https://console.example.test", false},
		{"path", "domain", "http://preview.example.test/x", "https://console.example.test", false},
		{"off", "off", "", "https://console.example.test", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := deployment{PublicURL: tc.public, PreviewMode: tc.mode, PreviewURL: tc.address}
			err := s.normalizeAddresses()
			if (err == nil) != tc.valid {
				t.Fatalf("normalize error=%v", err)
			}
			if tc.name == "default-off" && (s.PreviewMode != "off" || len(s.previewPorts()) != 0) {
				t.Fatal("new deployment opened previews")
			}
			if tc.name == "infer-domain" && s.PreviewMode != "domain" {
				t.Fatal("domain URL did not enable preview")
			}
		})
	}
}

func TestPreviewRenderPublishesOnlyGatewayAndPreservesProviderSettings(t *testing.T) {
	s := previewTestDeployment(t)
	s.PreviewURL = "http://preview.example.test:8081"
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
	if preview["mode"] != "domain" || preview["base_url"] != s.PreviewURL || preview["trusted_proxies"].([]any)[0] != "172.30.240.2/32" {
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
	if len(ports) != 2 {
		t.Fatal("unexpected published ports", ports)
	}
	for _, port := range []string{"8081:8081"} {
		if !strings.Contains(string(raw), port) {
			t.Fatal("missing port", port)
		}
	}
	if composition["networks"].(object)["preview_ingress"].(object)["internal"] != true {
		t.Fatal("preview ingress not isolated")
	}
	raw, _ = os.ReadFile(filepath.Join(s.Root, "gateway.conf"))
	text := string(raw)
	for _, expected := range []string{"proxy_pass http://backend-preview:8889; proxy_bind 172.30.240.2", "proxy_pass http://backend-preview:8888; proxy_bind 172.30.240.2", "proxy_set_header X-Forwarded-For $remote_addr", "listen 8081;"} {
		if !strings.Contains(text, expected) {
			t.Fatal("missing gateway contract", expected)
		}
	}
	if strings.Contains(text, "listen 8081 ssl") || strings.Contains(text, "$proxy_add_x_forwarded_for") {
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
	s.PreviewURL = "http://preview.example.test"
	s.TrustedProxyCIDRs = []string{"192.0.2.1/32"}
	next, err := previewOptions(s, options{})
	if err != nil {
		t.Fatal(err)
	}
	if next.PreviewURL != s.PreviewURL || next.PublicURL != s.PublicURL || len(next.TrustedProxyCIDRs) != 1 {
		t.Fatal("defaults overwrote saved state")
	}
	next, err = previewOptions(s, options{PreviewMode: "off", previewModeSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if next.PreviewURL != "" || len(next.previewPorts()) != 0 {
		t.Fatal("off still publishes previews")
	}
	if s.PreviewURL != "http://preview.example.test" {
		t.Fatal("changed input state")
	}
	secure := *s
	secure.PreviewURL = "https://preview.example.test"
	secure.PreviewSeparateTLS = true
	next, err = previewOptions(&secure, options{PreviewURL: "http://preview.example.test", previewURLSet: true})
	if err != nil || next.PreviewSeparateTLS {
		t.Fatal("cannot switch independent HTTPS preview to HTTP", err)
	}
}

func TestSeparatePreviewCertificateKeepsMainIdentity(t *testing.T) {
	issuer := previewTestDeployment(t)
	issuer.PublicURL = "https://192.0.2.11"
	issuer.PreviewURL = "https://preview.example.test"
	if err := prepareTLS(issuer, options{}); err != nil {
		t.Fatal(err)
	}
	s := previewTestDeployment(t)
	s.PreviewURL = "https://preview.example.test"
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
	bad.PreviewURL = "https://other.example.test"
	bad.PreviewSeparateTLS = true
	if prepareTLS(bad, o) == nil {
		t.Fatal("certificate for unrelated preview address accepted")
	}
}

func mockPreviewDNS(t *testing.T) {
	t.Helper()
	old := lookupPreviewHost
	lookupPreviewHost = func(_ context.Context, host string) ([]string, error) {
		if host == "preview.example.test" || strings.HasSuffix(host, ".preview.example.test") {
			return []string{"127.0.0.1"}, nil
		}
		return nil, errors.New("unexpected test host")
	}
	t.Cleanup(func() { lookupPreviewHost = old })
}

func TestPreviewGatewayProbeChecksServiceAndTLS(t *testing.T) {
	mockPreviewDNS(t)
	seen := []string{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/jingjiaagent-preview-health" {
			t.Error("wrong health path")
		}
		seen = append(seen, r.Host)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"service":"jingjiaagent-preview"}`))
	})
	issuer := previewTestDeployment(t)
	issuer.PreviewURL = "https://preview.example.test"
	if err := prepareTLS(issuer, options{}); err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(issuer.Root, "tls/server.crt"), filepath.Join(issuer.Root, "tls/server.key"))
	if err != nil {
		t.Fatal(err)
	}
	plain := httptest.NewServer(handler)
	defer plain.Close()
	secure := httptest.NewUnstartedServer(handler)
	secure.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	secure.StartTLS()
	defer secure.Close()
	for _, server := range []*httptest.Server{plain, secure} {
		u, _ := url.Parse(server.URL)
		pool := x509.NewCertPool()
		ca, _ := os.ReadFile(filepath.Join(issuer.Root, "tls/ca.crt"))
		pool.AppendCertsFromPEM(ca)
		s := &deployment{PreviewMode: "domain", PreviewURL: u.Scheme + "://preview.example.test:" + u.Port()}
		seen = nil
		if err := probePreview(s, pool); err != nil {
			t.Fatal(err)
		}
		if len(seen) != 2 || !strings.HasPrefix(seen[0], "preview.example.test:") || !strings.Contains(seen[1], ".preview.example.test:") {
			t.Fatal("base/wildcard not both probed", seen)
		}
		if server.TLS != nil && probePreview(s, x509.NewCertPool()) == nil {
			t.Fatal("untrusted TLS accepted")
		}
	}
	wrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"service":"other"}`)) }))
	defer wrong.Close()
	u, _ := url.Parse(wrong.URL)
	if probePreview(&deployment{PreviewMode: "domain", PreviewURL: "http://preview.example.test:" + u.Port()}, nil) == nil {
		t.Fatal("non-preview application accepted")
	}
}

func TestPreviewWildcardDNSRequired(t *testing.T) {
	mockPreviewDNS(t)
	s := &deployment{PreviewMode: "domain", PreviewURL: "http://preview.example.test"}
	lookupPreviewHost = func(_ context.Context, host string) ([]string, error) {
		if host == "preview.example.test" {
			return []string{"127.0.0.1"}, nil
		}
		return nil, errors.New("NXDOMAIN")
	}
	if checkPreviewDNS(s) == nil {
		t.Fatal("accepted missing wildcard DNS")
	}
	if checkPreviewDNS(&deployment{PreviewMode: "off"}) != nil {
		t.Fatal("disabled preview queried DNS")
	}
}

func TestPreviewLegacyConfigurationRequiresExplicitDecision(t *testing.T) {
	s := previewTestDeployment(t)
	s.PreviewMode, s.PreviewURL = "port", "http://192.0.2.10"
	if _, err := previewOptions(s, options{}); err == nil || !strings.Contains(err.Error(), "--preview-mode off") {
		t.Fatal("old IP deployment silently converted", err)
	}
	for _, o := range []options{
		{PreviewMode: "off", previewModeSet: true},
		{PreviewURL: "http://preview.example.test", previewURLSet: true},
	} {
		next, err := previewOptions(s, o)
		if err != nil {
			t.Fatal(err)
		}
		if next.PreviewMode == "port" {
			t.Fatal("old mode retained")
		}
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
	if runtime.GOOS == "linux" && checkPreviewPortAvailability(&deployment{}, &deployment{PreviewMode: "domain", PreviewURL: "http://preview.example.test:" + port}) == nil {
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

func TestPreviewTLSRequiresWildcardNotOneNamedChild(t *testing.T) {
	s := previewTestDeployment(t)
	s.PreviewURL = "https://preview.example.test"
	if err := prepareTLS(s, options{}); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(s.Root, "tls/server.crt"), filepath.Join(s.Root, "tls/server.key"))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	caPair, err := tls.LoadX509KeyPair(filepath.Join(s.Root, "tls/ca.crt"), filepath.Join(s.Root, "tls/ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caPair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	leaf.DNSNames = []string{"preview.example.test", "preview-certificate-check.preview.example.test"}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, leaf.PublicKey, caPair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, "named-child.crt")
	if err = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = validatePreviewTLS(s, path, filepath.Join(s.Root, "tls/server.key")); err == nil || !strings.Contains(err.Error(), "通配") {
		t.Fatal("one named child accepted as wildcard", err)
	}
}

func TestPreviewProbePreservesOriginPortForm(t *testing.T) {
	mockPreviewDNS(t)
	for _, origin := range []string{"http://preview.example.test", "https://preview.example.test", "https://preview.example.test:9444"} {
		s := &deployment{PreviewMode: "domain", PreviewURL: origin}
		endpoints, err := previewEndpoints(s)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(origin)
		if len(endpoints) != 2 || endpoints[1].url.Port() != u.Port() {
			t.Fatal("probe changed Host port form", endpoints)
		}
	}
}
