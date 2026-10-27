package config

import (
	"github.com/google/uuid"
	"testing"
)

func TestPreviewConfigModesAndProtocols(t *testing.T) {
	for _, mode := range []string{"", "domain"} {
		for _, scheme := range []string{"http", "https"} {
			value := RuntimePreview{Mode: mode, BaseURL: scheme + "://preview.example.net", Listen: "0.0.0.0:8889", TrustedProxies: []string{"172.31.2.2/32"}}
			if err := value.Validate("https://app.example.net"); err != nil {
				t.Fatal(mode, scheme, err)
			}
		}
	}
	valid := RuntimePreview{Mode: "domain", BaseURL: "http://preview.example.net:8080", Listen: "0.0.0.0:8889", TrustedProxies: []string{"172.31.2.2/32"}}
	if err := valid.Validate("https://app.example.net"); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"", "https://app.example.net", "https://example.net", "https://127.0.0.1", "http://192.0.2.10", "https://[::1]", "https://*.preview.example.net", "https://preview.example.net/path", "https://user:secret@preview.example.net", "https://preview.example.net?token=x", "https://preview.example.net:65536"} {
		bad := valid
		bad.BaseURL = base
		if bad.Validate("https://app.example.net") == nil {
			t.Fatal("invalid domain accepted", base)
		}
	}
	for _, change := range []func(*RuntimePreview){
		func(p *RuntimePreview) { p.TrustedProxies = nil },
		func(p *RuntimePreview) { p.TrustedProxies = []string{"*"} },
		func(p *RuntimePreview) { p.Mode = "unknown" },
		func(p *RuntimePreview) { p.Listen = "0.0.0.0:65536" },
		func(p *RuntimePreview) { p.Mode = "port" },
	} {
		bad := valid
		change(&bad)
		if bad.Validate("https://app.example.net") == nil {
			t.Fatal("invalid preview config accepted", bad)
		}
	}
	if (RuntimePreview{}).EffectiveMode() != "off" || (RuntimePreview{BaseURL: "http://preview.example.net"}).EffectiveMode() != "domain" {
		t.Fatal("incorrect default mode")
	}
	for _, disabled := range []RuntimePreview{{}, {Mode: "off"}, {Mode: "off", BaseURL: valid.BaseURL}} {
		if disabled.EffectiveMode() != "off" || disabled.Validate("https://app.example.net") != nil {
			t.Fatal("preview must be optional", disabled)
		}
	}
}

func TestInstallableNodeRequiresFixedScopeAndTLS(t *testing.T) {
	valid := func() Runtime {
		return Runtime{Backend: "agent_compose", Experimental: true, PayloadKeyFile: "payload.key", InstallerManifestFile: "manifest.json", Nodes: []RuntimeNode{{ID: uuid.NewString(), URL: "https://runtime.example", TokenFile: "token", OwnerID: uuid.NewString(), CAFile: "ca", Install: true, InstallListen: "0.0.0.0:7443", InstallCertFile: "cert", InstallKeyFile: "key"}}}
	}
	r := valid()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Runtime){
		func(r *Runtime) { r.Nodes[0].URL = "http://127.0.0.1:7410" },
		func(r *Runtime) { r.Nodes[0].ID = "not-a-canonical-uuid" },
		func(r *Runtime) { r.Nodes[0].OwnerID = "" },
		func(r *Runtime) { r.Nodes[0].InstallKeyFile = "" },
		func(r *Runtime) { r.InstallerManifestFile = "" },
		func(r *Runtime) { r.Nodes[0].InstallListen = "0.0.0.0:65536" },
		func(r *Runtime) { r.Nodes[0].InstallListen = "example.com:7443" },
		func(r *Runtime) { r.InstallerBaseURL = "http://public.example" },
		func(r *Runtime) { r.InstallerBaseURL = "https://user:secret@example.com" },
	} {
		r := valid()
		change(&r)
		if r.Validate() == nil {
			t.Fatal("unsafe node installation configuration accepted")
		}
	}
}

func TestRuntimeValidation(t *testing.T) {
	if err := (&Runtime{}).Validate(); err != nil {
		t.Fatal(err)
	}
	valid := func() Runtime {
		return Runtime{Backend: "agent_compose", Experimental: true, PayloadKeyFile: "key", Nodes: []RuntimeNode{{ID: "node-1", URL: "http://127.0.0.1:7410", TokenFile: "token"}}}
	}
	cases := []struct {
		name   string
		change func(*Runtime)
	}{
		{"production gate", func(r *Runtime) { r.Experimental = false }},
		{"unknown backend", func(r *Runtime) { r.Backend = "unknown" }},
		{"plaintext remote", func(r *Runtime) { r.Nodes[0].URL = "http://192.168.1.2:7410" }},
		{"credentials in URL", func(r *Runtime) { r.Nodes[0].URL = "https://user:password@node" }},
		{"query in URL", func(r *Runtime) { r.Nodes[0].URL = "https://node?token=x" }},
		{"duplicate node", func(r *Runtime) { r.Nodes = append(r.Nodes, r.Nodes[0]) }},
		{"polling bounds", func(r *Runtime) { r.PollInterval = "1ms" }},
		{"environment ID used as host", func(r *Runtime) { r.Nodes[0].ID = "agent_123" }},
		{"invalid owner", func(r *Runtime) { r.Nodes[0].OwnerID = "invalid" }},
		{"team without owner", func(r *Runtime) { r.Nodes[0].TeamID = "11111111-1111-4111-8111-111111111111" }},
	}
	r := valid()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	t.Run("nodes can be enrolled after startup", func(t *testing.T) {
		r := valid()
		r.Nodes = nil
		if err := r.Validate(); err != nil {
			t.Fatalf("empty bootstrap nodes must allow database-backed enrollment: %v", err)
		}
	})
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r := valid()
			tt.change(&r)
			if r.Validate() == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestRuntimeMCPURLConfiguration(t *testing.T) {
	t.Setenv("JINGJIAAGENT_RUNTIME_BACKEND", "taskflow")
	for _, test := range []struct {
		url          string
		experimental bool
		valid        bool
	}{
		{"", false, true},
		{"https://gateway.internal/mcp", false, true},
		{"http://127.0.0.1:47420/mcp", false, true},
		{"http://host.docker.internal:47420/mcp", true, true},
		{"http://host.docker.internal:47420/mcp", false, false},
		{"http://backend:47424/mcp", true, true},
		{"http://backend:47424/mcp", false, false},
		{"http://backend:47425/mcp", true, false},
		{"http://backend/mcp", true, false},
		{"http://backend:47424/other", true, false},
		{"http://backend.example:47424/mcp", true, false},
		{"http://gateway.internal/mcp", true, false},
		{"https://user:secret@gateway.internal/mcp", false, false},
		{"https://gateway.internal/mcp?token=secret", false, false},
		{"https://gateway.internal/mcp#fragment", false, false},
		{"file:///mcp", false, false},
	} {
		r := Runtime{MCPURL: test.url, Experimental: test.experimental}
		if (r.Validate() == nil) != test.valid {
			t.Fatal("runtime MCP URL validation does not enforce the configured transport boundary")
		}
	}
	t.Setenv("JINGJIAAGENT_RUNTIME_MCP_URL", " https://gateway.internal/mcp ")
	cfg, err := Init(t.TempDir())
	if err != nil || cfg.Runtime.MCPURL != "https://gateway.internal/mcp" {
		t.Fatal("runtime MCP URL was not loaded and normalized")
	}
}
