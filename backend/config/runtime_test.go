package config

import (
	"github.com/google/uuid"
	"testing"
)

func TestPreviewConfigRequiresDedicatedWildcardHost(t *testing.T) {
	valid := RuntimePreview{BaseURL: "https://preview.example.net", Listen: "127.0.0.1:7421", TrustedProxies: []string{"10.0.0.0/8"}}
	if err := valid.Validate("https://app.example.net"); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"https://app.example.net", "https://example.net", "http://preview.example.net", "https://127.0.0.1:7421", "https://preview.example.net/path", "https://user:secret@preview.example.net", "https://preview.example.net?token=x"} {
		bad := valid
		bad.BaseURL = base
		if bad.Validate("https://app.example.net") == nil {
			t.Fatal("invalid preview host accepted", base)
		}
	}
	bad := valid
	bad.TrustedProxies = []string{"*"}
	if bad.Validate("https://app.example.net") == nil {
		t.Fatal("invalid proxy trust accepted")
	}
	local := RuntimePreview{BaseURL: "http://localhost:47421", Listen: "127.0.0.1:47421"}
	if err := local.Validate("http://127.0.0.1:47420"); err != nil {
		t.Fatal(err)
	}
	local.Listen = "0.0.0.0:47421"
	if local.Validate("http://127.0.0.1:47420") == nil {
		t.Fatal("plaintext preview exposed beyond loopback")
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
		{"empty nodes", func(r *Runtime) { r.Nodes = nil }},
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
	t.Setenv("MCAI_RUNTIME_MCP_URL", " https://gateway.internal/mcp ")
	cfg, err := Init(t.TempDir())
	if err != nil || cfg.Runtime.MCPURL != "https://gateway.internal/mcp" {
		t.Fatal("runtime MCP URL was not loaded and normalized")
	}
}
