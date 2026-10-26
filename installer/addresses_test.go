package main

import (
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConsoleAndInternalAddresses(t *testing.T) {
	for _, tc := range []struct {
		public, internal, preview string
		valid                     bool
	}{
		{"http://192.168.1.2", "", "", true},
		{"https://agent.internal", "", "", true},
		{"http://agent.internal:8080", "https://node.internal:9443", "", true},
		{"http://[fd00::12]:8080", "https://[fd00::12]:9443", "", true},
		{"http://agent.internal:8443", "", "", false},
		{"http://agent.internal", "https://node.internal:80", "", false},
		{"http://agent.internal:443", "", "https://preview.internal", false},
		{"http://agent.internal", "https://nodes.internal", "https://preview.internal", true},
		{"http://agent.internal", "", "https://preview.internal/", false},
		{"http://agent.internal", "", "https://preview.internal:443", true},
		{"http://agent.internal", "https://nodes.preview.internal", "https://preview.internal", false},
		{"http://agent.internal", "https://preview.internal", "https://preview.internal", false},
		{"http://agent.internal", "http://node.internal", "", false},
		{"http://user:secret@agent.internal", "", "", false},
		{"http://agent.internal:65536", "", "", false},
		{"http://agent.internal:080", "", "", false},
		{"http://agent.internal/app", "", "", false},
		{"http://agent.internal?", "", "", false},
		{"http://agent.internal", "https://node.internal/api", "", false},
		{"ftp://agent.internal", "", "", false},
	} {
		if err := validateAddresses(tc.public, tc.internal, tc.preview); (err == nil) != tc.valid {
			t.Errorf("%s / %s: %v", tc.public, tc.internal, err)
		}
	}
	if internalURL("https://agent.internal:9443", "") != "https://agent.internal:9443" {
		t.Fatal("legacy HTTPS origin changed")
	}
	if internalURL("http://agent.internal:8080", "") != "https://agent.internal:8443" {
		t.Fatal("HTTP must derive a separate TLS origin")
	}
	if _, err := httpsURL("http://release.internal"); err == nil {
		t.Fatal("HTTP console must not permit plaintext release downloads")
	}
}

func TestDeploymentOriginsSurviveRenderStateAndRetry(t *testing.T) {
	for _, tc := range []struct{ public, internal, wantPublic, wantInternal string }{
		{"http://Agent.Internal:80/", "", "http://agent.internal", "https://agent.internal:8443"},
		{"https://Agent.Internal:443/", "", "https://agent.internal", "https://agent.internal"},
		{"http://Agent.Internal:8080", "https://Node.Internal:443/", "http://agent.internal:8080", "https://node.internal"},
		{"http://[FD00:0:0:0:0:0:0:12]:80", "https://[FD00::12]:9443", "http://[fd00::12]", "https://[fd00::12]:9443"},
	} {
		t.Run(tc.public, func(t *testing.T) {
			s := &deployment{PreviewSubnet: "172.30.240.0/29", Schema: 1, Root: t.TempDir(), Data: filepath.Join(t.TempDir(), "data"), PublicURL: tc.public, InternalURL: tc.internal, Mode: "controller", Project: "origins-test", Images: map[string]imageRecord{}}
			// Read old state as well as accepting equivalent retry arguments.
			if err := saveState(s); err != nil {
				t.Fatal(err)
			}
			loaded, err := readState(s.Root)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.PublicURL != tc.wantPublic || loaded.InternalURL != tc.wantInternal {
				t.Fatalf("loaded origins = %q / %q", loaded.PublicURL, loaded.InternalURL)
			}
			if err := validateRetryOptions(s, options{PublicURL: loaded.PublicURL, InternalURL: loaded.InternalURL, Mode: s.Mode, Data: s.Data, Project: s.Project}); err != nil {
				t.Fatal("equivalent retry rejected", err)
			}
			if err := render(s); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(s.Root, "config/server/config.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var cfg object
			if err := json.Unmarshal(raw, &cfg); err != nil {
				t.Fatal(err)
			}
			if cfg["server"].(object)["base_url"] != tc.wantPublic || cfg["runtime"].(object)["installer_base_url"] != tc.wantInternal {
				t.Fatal("rendered configuration did not normalize origins")
			}
			if err := validateRetryOptions(s, options{PublicURL: tc.wantPublic + "/other", InternalURL: tc.wantInternal, Mode: s.Mode, Data: s.Data, Project: s.Project}); err == nil {
				t.Fatal("normalization accepted an invalid retry origin")
			}
		})
	}
}

func TestHTTPDeploymentKeepsNodeAndWorkspaceTrafficEncrypted(t *testing.T) {
	s := &deployment{PreviewSubnet: "172.30.240.0/29", Schema: 1, Root: t.TempDir(), Data: filepath.Join(t.TempDir(), "data"), PublicURL: "http://console.internal:8080", InternalURL: "https://nodes.internal:9443", Mode: "single", Project: "http-test", Images: map[string]imageRecord{}}
	if err := prepareTLS(s, options{}); err != nil {
		t.Fatal(err)
	}
	if err := render(s); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(s.Root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	var cfg object
	if err := json.Unmarshal([]byte(read("config/server/config.yaml")), &cfg); err != nil {
		t.Fatal(err)
	}
	for section, expected := range map[string]object{
		"server":         {"base_url": s.PublicURL},
		"runtime":        {"installer_base_url": s.InternalURL, "mcp_url": s.InternalURL + "/mcp"},
		"llm_proxy":      {"base_url": s.InternalURL},
		"object_storage": {"access_endpoint": s.PublicURL + "/oss", "agent_access_endpoint": s.InternalURL + "/oss"},
		"deployment":     {"local_node_url": "https://runtime-proxy:7411"},
	} {
		for key, value := range expected {
			if cfg[section].(object)[key] != value {
				t.Fatalf("%s.%s = %v", section, key, cfg[section].(object)[key])
			}
		}
	}
	proxy := read("gateway.conf")
	if !strings.Contains(proxy, "listen 8080;") || !strings.Contains(proxy, "listen 9443 ssl;") || strings.Contains(proxy, "listen 8080 ssl") {
		t.Fatal("wrong gateway listeners")
	}
	if !strings.Contains(proxy, "location /v1/ { return 426;") || !strings.Contains(proxy, "proxy_set_header X-Forwarded-Proto $scheme") {
		t.Fatal("plaintext node requests or wrong forwarded scheme")
	}
	if err := validateTLS(s.InternalURL, filepath.Join(s.Root, "tls/server.crt"), filepath.Join(s.Root, "tls/server.key")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read("compose.json"), "9443:9443") || !strings.Contains(read("compose.json"), "8080:8080") {
		t.Fatal("origins not published")
	}
	// Re-render must repair an accidental public URL in a machine setting while
	// preserving unrelated integrations and the previously generated trust root.
	ca := read("tls/ca.crt")
	cfg["llm_proxy"].(object)["base_url"] = s.PublicURL
	cfg["work"].(object)["skillhub"] = object{"url": "https://hub.internal"}
	if err := writeJSON(filepath.Join(s.Root, "config/server/config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	if err := render(s); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(read("config/server/config.yaml")), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["llm_proxy"].(object)["base_url"] != s.InternalURL || !strings.Contains(read("config/server/config.yaml"), "hub.internal") || ca != read("tls/ca.crt") {
		t.Fatal("re-render changed settings or lost TLS separation")
	}
	if err := saveState(s); err != nil {
		t.Fatal(err)
	}
	loaded, err := readState(s.Root)
	if err != nil || loaded.internalURL() != s.InternalURL {
		t.Fatal("state lost internal origin", err)
	}
	if err := validateRetryOptions(s, options{PublicURL: s.PublicURL, InternalURL: "https://other.internal", Mode: s.Mode, Data: s.Data, Project: s.Project}); err == nil {
		t.Fatal("retry allowed changing node identity")
	}
	if _, err := renewedCertificates(s); err != nil {
		t.Fatal("HTTP deployment cannot renew internal TLS", err)
	}
}

func TestDoctorChecksTLSRatherThanFallingBackToHTTP(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"enabled":true,"initialized":true}}`))
	})
	plain := httptest.NewServer(handler)
	defer plain.Close()
	encrypted := httptest.NewTLSServer(handler)
	defer encrypted.Close()
	pool := x509.NewCertPool()
	pool.AddCert(encrypted.Certificate())
	for _, address := range []string{plain.URL, encrypted.URL} {
		if initialized, err := probeDeploymentAPI(address, pool); err != nil || !initialized {
			t.Fatal(address, initialized, err)
		}
	}
	if _, err := probeDeploymentAPI(encrypted.URL, x509.NewCertPool()); err == nil {
		t.Fatal("untrusted TLS accepted")
	}
}
