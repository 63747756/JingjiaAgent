package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Secrets are file references: main logs Config at debug level.
type Runtime struct {
	Backend               string          `mapstructure:"backend"`
	Experimental          bool            `mapstructure:"experimental"`
	DeploymentManaged     bool            `mapstructure:"deployment_managed"`
	PlatformCAFile        string          `mapstructure:"platform_ca_file"`
	PayloadKeyFile        string          `mapstructure:"payload_key_file"`
	NodesJSON             string          `mapstructure:"nodes_json"`
	Nodes                 []RuntimeNode   `mapstructure:"nodes"`
	PollInterval          string          `mapstructure:"poll_interval"`
	CallbackBaseURL       string          `mapstructure:"callback_base_url"`
	MCPURL                string          `mapstructure:"mcp_url"`
	Preview               RuntimePreview  `mapstructure:"preview"`
	InstallerManifestFile string          `mapstructure:"installer_manifest_file"`
	InstallerBinaryFile   string          `mapstructure:"installer_binary_file"`
	InstallerBaseURL      string          `mapstructure:"installer_base_url"`
	Capacity              RuntimeCapacity `mapstructure:"capacity"`
}

// BaseURL is the employee-facing origin. Port mode replaces its port with an
// allocated public port; domain mode prefixes its hostname with a mapping ID.
type RuntimePreview struct {
	Mode           string   `mapstructure:"mode"`
	BaseURL        string   `mapstructure:"base_url"`
	PortRange      string   `mapstructure:"port_range"`
	Listen         string   `mapstructure:"listen"`
	TrustedProxies []string `mapstructure:"trusted_proxies"`
}

func (p RuntimePreview) EffectiveMode() string {
	if p.Mode == "off" || (p.Mode == "" && p.BaseURL == "") {
		return "off"
	}
	if p.Mode == "" {
		return "port"
	}
	return p.Mode
}

// PreviewPortRange is bounded to avoid accidentally publishing every host port.
func PreviewPortRange(value string) (int, int, error) {
	if value == "" {
		value = "31000-31099"
	}
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return 0, 0, errors.New("preview port range must be START-END")
	}
	first, e1 := strconv.Atoi(parts[0])
	last, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil || first < 1024 || last > 65535 || first > last || last-first >= 1000 {
		return 0, 0, errors.New("preview port range requires 1 to 1000 ports between 1024 and 65535")
	}
	return first, last, nil
}

func (p RuntimePreview) Validate(platformURL string) error {
	mode := p.EffectiveMode()
	if mode == "off" {
		return nil
	}
	if mode != "port" && mode != "domain" {
		return errors.New("runtime.preview.mode must be port, domain or off")
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(u.Host, "*%\\") {
		return errors.New("runtime.preview requires an origin without path or credentials")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return errors.New("runtime.preview requires HTTP or HTTPS")
	}
	listenHost, listenPort, err := net.SplitHostPort(p.Listen)
	lp, pe := strconv.Atoi(listenPort)
	if err != nil || net.ParseIP(listenHost) == nil || pe != nil || lp < 1 || lp > 65535 {
		return errors.New("runtime.preview.listen requires host:port")
	}
	main, err := url.Parse(platformURL)
	if err != nil || main.Host == "" {
		return errors.New("invalid platform URL")
	}
	if mode == "domain" {
		if net.ParseIP(u.Hostname()) != nil || strings.EqualFold(main.Hostname(), u.Hostname()) || strings.HasSuffix(strings.ToLower(main.Hostname()), "."+strings.ToLower(u.Hostname())) {
			return errors.New("domain previews require a separate wildcard domain from the platform")
		}
		if u.Port() != "" {
			if port, err := strconv.Atoi(u.Port()); err != nil || port < 1 || port > 65535 {
				return errors.New("invalid preview domain port")
			}
		}
	} else {
		if u.Port() != "" {
			return errors.New("port preview base URL must not include a port")
		}
		first, last, err := PreviewPortRange(p.PortRange)
		if err != nil {
			return err
		}
		mainPort := 80
		if main.Scheme == "https" {
			mainPort = 443
		}
		if main.Port() != "" {
			mainPort, _ = strconv.Atoi(main.Port())
		}
		if mainPort >= first && mainPort <= last {
			return errors.New("preview port pool overlaps the platform port")
		}
	}
	if len(p.TrustedProxies) == 0 {
		return errors.New("preview gateway requires explicit trusted ingress addresses")
	}
	for _, cidr := range p.TrustedProxies {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return errors.New("invalid runtime.preview.trusted_proxies CIDR")
		}
	}
	return nil
}

type RuntimeNode struct {
	Name                string `mapstructure:"-" json:"name,omitempty"`
	ID                  string `mapstructure:"id" json:"id"`
	URL                 string `mapstructure:"url" json:"url"`
	TokenFile           string `mapstructure:"token_file" json:"token_file"`
	CAFile              string `mapstructure:"ca_file" json:"ca_file"`
	GuestImage          string `mapstructure:"guest_image" json:"guest_image"`
	InstalledGuestImage string `mapstructure:"-" json:"installed_guest_image,omitempty"`
	// Verified by the full-host recovery installer; never accepted from task input.
	RecoveredImages map[string]string `mapstructure:"-" json:"recovered_images,omitempty"`
	OwnerID         string            `mapstructure:"owner_id" json:"owner_id,omitempty"`
	TeamID          string            `mapstructure:"team_id" json:"team_id,omitempty"`
	Install         bool              `mapstructure:"install" json:"install,omitempty"`
	InstallListen   string            `mapstructure:"install_listen" json:"install_listen,omitempty"`
	InstallCertFile string            `mapstructure:"install_cert_file" json:"install_cert_file,omitempty"`
	InstallKeyFile  string            `mapstructure:"install_key_file" json:"install_key_file,omitempty"`
}

// Limits apply to the Docker engine pool, including all configured node aliases.
type RuntimeCapacity struct {
	Enabled            bool  `mapstructure:"enabled"`
	ReserveCPUMillis   int64 `mapstructure:"reserve_cpu_millis"`
	ReserveMemoryBytes int64 `mapstructure:"reserve_memory_bytes"`
	MaxCPUMillis       int64 `mapstructure:"max_cpu_millis"`
	MaxMemoryBytes     int64 `mapstructure:"max_memory_bytes"`
}

func (r *Runtime) Validate() error {
	r.CallbackBaseURL = strings.TrimSpace(r.CallbackBaseURL)
	if r.CallbackBaseURL != "" {
		u, err := url.Parse(r.CallbackBaseURL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && net.ParseIP(u.Hostname()).IsLoopback())) {
			return errors.New("runtime.callback_base_url requires HTTPS or an HTTP loopback endpoint without path or credentials")
		}
	}
	if r.Capacity.ReserveCPUMillis < 0 || r.Capacity.ReserveMemoryBytes < 0 || r.Capacity.MaxCPUMillis < 0 || r.Capacity.MaxMemoryBytes < 0 || r.Capacity.ReserveCPUMillis > 65536000 || r.Capacity.MaxCPUMillis > 65536000 {
		return errors.New("runtime.capacity requires nonnegative CPU millicores and memory bytes within supported limits")
	}
	if r.InstallerBaseURL != "" {
		u, err := url.Parse(r.InstallerBaseURL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && (net.ParseIP(u.Hostname()).IsLoopback() || (r.Experimental && u.Hostname() == "host.docker.internal")))) {
			return errors.New("runtime.installer_base_url requires HTTPS or an experimental local endpoint")
		}
	}
	if r.Backend == "" {
		r.Backend = "taskflow"
	}
	if r.Backend != "taskflow" && r.Backend != "agent_compose" {
		return errors.New("runtime.backend must be taskflow or agent_compose")
	}
	r.MCPURL = strings.TrimSpace(r.MCPURL)
	if r.MCPURL != "" {
		u, err := url.Parse(r.MCPURL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("invalid runtime.mcp_url")
		}
		ip := net.ParseIP(u.Hostname())
		local := ip != nil && ip.IsLoopback()
		// The isolated Linux acceptance stack exposes only this explicit MCP
		// endpoint to Guests; arbitrary plaintext Docker/LAN hosts stay rejected.
		composeFixture := u.Host == "backend:47424" && u.Path == "/mcp"
		dockerFixture := r.Experimental && (u.Hostname() == "host.docker.internal" || composeFixture)
		if u.Scheme != "https" && !(u.Scheme == "http" && (local || dockerFixture)) {
			return errors.New("runtime.mcp_url requires HTTPS; loopback HTTP and experimental Docker host access are allowed for local validation")
		}
	}
	if r.NodesJSON != "" {
		if err := json.Unmarshal([]byte(r.NodesJSON), &r.Nodes); err != nil {
			return errors.New("invalid runtime.nodes_json")
		}
	}
	if r.Backend == "taskflow" && len(r.Nodes) == 0 {
		return nil
	}
	if r.DeploymentManaged {
		u, err := url.Parse(r.InstallerBaseURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || r.PlatformCAFile == "" || r.PayloadKeyFile == "" {
			return errors.New("managed deployment requires HTTPS, platform CA and persistent payload key")
		}
	}
	if r.Backend == "agent_compose" && !r.Experimental && !r.DeploymentManaged {
		return errors.New("agent_compose production activation is blocked until remote parity is accepted; experimental mode is required for integration validation")
	}
	if r.PayloadKeyFile == "" {
		return errors.New("runtime payload key file is required")
	}
	if r.PollInterval == "" {
		r.PollInterval = "1s"
	}
	d, err := time.ParseDuration(r.PollInterval)
	if err != nil || d < 100*time.Millisecond || d > time.Minute {
		return errors.New("runtime.poll_interval must be between 100ms and 1m")
	}
	seen := map[string]bool{}
	for _, n := range r.Nodes {
		if strings.TrimSpace(n.ID) == "" || seen[n.ID] {
			return errors.New("runtime node IDs must be nonempty and unique")
		}
		seen[n.ID] = true
		if strings.HasPrefix(n.ID, "agent_") || len(n.ID) > 128 {
			return errors.New("runtime node ID is not a host ID")
		}
		for _, identity := range []string{n.OwnerID, n.TeamID} {
			if identity != "" {
				id, err := uuid.Parse(identity)
				if err != nil || id == uuid.Nil || id.String() != identity {
					return errors.New("runtime node registration IDs must be canonical UUIDs")
				}
			}
		}
		if n.TeamID != "" && n.OwnerID == "" {
			return errors.New("runtime node team registration requires owner_id")
		}
		u, err := url.Parse(n.URL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("invalid runtime node URL for %s", n.ID)
		}
		if u.Scheme != "https" && !(u.Scheme == "http" && net.ParseIP(u.Hostname()) != nil && net.ParseIP(u.Hostname()).IsLoopback()) {
			return fmt.Errorf("runtime node %s requires HTTPS or loopback HTTP", n.ID)
		}
		if n.TokenFile == "" {
			return fmt.Errorf("runtime node %s requires token_file", n.ID)
		}
		if n.Install {
			id, err := uuid.Parse(n.ID)
			if err != nil || id == uuid.Nil || id.String() != n.ID || n.OwnerID == "" || r.InstallerManifestFile == "" || u.Scheme != "https" || n.CAFile == "" || n.InstallCertFile == "" || n.InstallKeyFile == "" {
				return errors.New("installable nodes require a canonical ID, owner, bundle manifest and HTTPS certificate files")
			}
			host, port, err := net.SplitHostPort(n.InstallListen)
			p, pe := strconv.Atoi(port)
			if err != nil || net.ParseIP(host) == nil || pe != nil || p < 1 || p > 65535 {
				return errors.New("runtime node install_listen requires an explicit IP and valid port")
			}
		}
	}
	return nil
}
