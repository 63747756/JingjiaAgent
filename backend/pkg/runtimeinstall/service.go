package runtimeinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/domain"
	"github.com/chaitin/MonkeyCode/backend/errcode"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/samber/do"
)

//go:embed install.sh.tmpl
var installTemplate string

type Authorization interface {
	AuthorizeRuntimeInstall(context.Context, string, string, config.RuntimeNode) error
}
type Confirmation interface {
	ConfirmNodeInstallation(context.Context, string, string, string) (bool, error)
}
type Manifest struct {
	Schema       int    `json:"schema"`
	Architecture string `json:"architecture"`
	Archive      string `json:"archive"`
	SHA256       string `json:"sha256"`
	DaemonImage  string `json:"daemon_image"`
	GuestImage   string `json:"guest_image"`
	ProxyImage   string `json:"proxy_image"`
}
type Service struct {
	cfg          *config.Config
	redis        *redis.Client
	auth         Authorization
	confirmation Confirmation
	manifest     Manifest
	archive      string
	archiveInfo  os.FileInfo
}
type ticket struct {
	Actor string `json:"actor"`
	Team  string `json:"team"`
	Node  string `json:"node"`
	Phase string `json:"phase"`
}

var imageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func NewService(i *do.Injector) (*Service, error) {
	cfg := do.MustInvoke[*config.Config](i)
	auth, _ := do.MustInvoke[domain.HostRepo](i).(Authorization)
	confirmation, _ := do.MustInvoke[taskflow.Clienter](i).(Confirmation)
	return New(cfg, do.MustInvoke[*redis.Client](i), auth, confirmation)
}

func New(cfg *config.Config, rdb *redis.Client, auth Authorization, confirmation Confirmation) (*Service, error) {
	s := &Service{cfg: cfg, redis: rdb, auth: auth, confirmation: confirmation}
	if cfg.Runtime.Backend != "agent_compose" || cfg.Runtime.InstallerManifestFile == "" {
		return s, nil
	}
	if auth == nil || confirmation == nil || rdb == nil {
		return nil, errors.New("runtime installer dependencies unavailable")
	}
	u, endpointErr := url.Parse(s.platformURL())
	if endpointErr != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && (net.ParseIP(u.Hostname()).IsLoopback() || (cfg.Runtime.Experimental && u.Hostname() == "host.docker.internal")))) {
		return nil, errors.New("runtime installer platform endpoint requires HTTPS; local HTTP is only supported for validation")
	}
	b, err := readPrivate(cfg.Runtime.InstallerManifestFile)
	if err != nil {
		return nil, errors.New("cannot read runtime installer manifest")
	}
	if json.Unmarshal(b, &s.manifest) != nil {
		return nil, errors.New("invalid runtime installer manifest")
	}
	m := s.manifest
	hash, err := hex.DecodeString(m.SHA256)
	if m.Schema != 1 || (m.Architecture != "amd64" && m.Architecture != "arm64") || m.Archive == "" || filepath.Base(m.Archive) != m.Archive || m.Archive == "." || err != nil || len(hash) != 32 || !imageID.MatchString(m.DaemonImage) || !imageID.MatchString(m.GuestImage) || !imageID.MatchString(m.ProxyImage) {
		return nil, errors.New("runtime installer requires a versioned manifest, archive checksum and immutable image IDs")
	}
	s.archive = filepath.Join(filepath.Dir(cfg.Runtime.InstallerManifestFile), m.Archive)
	f, err := os.Open(s.archive)
	if err != nil {
		return nil, errors.New("runtime installer archive unavailable")
	}
	defer f.Close()
	s.archiveInfo, err = f.Stat()
	if err != nil || !s.archiveInfo.Mode().IsRegular() {
		return nil, errors.New("runtime installer archive must be a regular file")
	}
	digest := sha256.New()
	if _, err = io.Copy(digest, f); err != nil || hex.EncodeToString(digest.Sum(nil)) != m.SHA256 {
		return nil, errors.New("runtime installer archive checksum mismatch")
	}
	for _, node := range cfg.Runtime.Nodes {
		if node.Install {
			if _, err := s.nodePayload(node); err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}
func (s *Service) available() bool { return s != nil && s.archiveInfo != nil }
func (s *Service) Command(ctx context.Context, actor, team string) (string, error) {
	if !s.available() {
		return "", errcode.ErrRuntimeNodeInstaller
	}
	candidates := []config.RuntimeNode{}
	for _, node := range s.cfg.Runtime.Nodes {
		if node.Install && node.TeamID == team && (team != "" || node.OwnerID == actor) {
			if err := s.auth.AuthorizeRuntimeInstall(ctx, actor, team, node); err == nil {
				candidates = append(candidates, node)
			}
		}
	}
	if len(candidates) == 0 {
		return "", errcode.ErrRuntimeInstallScope
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	token := uuid.NewString()
	value, _ := json.Marshal(ticket{Actor: actor, Team: team, Node: candidates[0].ID, Phase: "issued"})
	if err := s.redis.Set(ctx, ticketKey(token), value, 2*time.Hour).Err(); err != nil {
		return "", err
	}
	u, err := url.Parse(s.platformURL())
	if err != nil || u.Host == "" || u.User != nil {
		return "", errors.New("invalid installer platform URL")
	}
	u = u.JoinPath("/api/v1/users/hosts/install")
	u.RawQuery = url.Values{"token": {token}}.Encode()
	bootstrap := fmt.Sprintf(`set -euo pipefail; script=$(curl --fail --silent --show-error --max-time 60 %s); bash -c "$script"`, shellQuote(u.String()))
	return "bash -c " + shellQuote(bootstrap), nil
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func ticketKey(token string) string {
	hash := sha256.Sum256([]byte(token))
	return "host:runtime-install:" + hex.EncodeToString(hash[:])
}
func (s *Service) load(ctx context.Context, token string, complete bool) (ticket, config.RuntimeNode, error) {
	var t ticket
	var node config.RuntimeNode
	if !s.available() {
		return t, node, errcode.ErrRuntimeNodeInstaller
	}
	id, err := uuid.Parse(token)
	if err != nil || id == uuid.Nil || id.String() != token {
		return t, node, errcode.ErrRuntimeInstallToken
	}
	value, err := s.redis.Get(ctx, ticketKey(token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return t, node, errcode.ErrRuntimeInstallToken
	}
	if err != nil {
		return t, node, err
	}
	if json.Unmarshal(value, &t) != nil || (t.Phase != "issued" && !(complete && t.Phase == "complete")) {
		return t, node, errcode.ErrRuntimeInstallToken
	}
	for _, n := range s.cfg.Runtime.Nodes {
		if n.ID == t.Node && n.Install {
			node = n
			break
		}
	}
	if node.ID == "" {
		return t, node, errcode.ErrRuntimeInstallToken
	}
	if err := s.auth.AuthorizeRuntimeInstall(ctx, t.Actor, t.Team, node); err != nil {
		return t, node, err
	}
	return t, node, nil
}

type payload struct {
	NodeID      string   `json:"node_id"`
	Listen      string   `json:"listen"`
	PlatformURL string   `json:"platform_url"`
	Token       string   `json:"node_token"`
	CA          string   `json:"ca"`
	Cert        string   `json:"cert"`
	Key         string   `json:"key"`
	Ticket      string   `json:"ticket"`
	Manifest    Manifest `json:"manifest"`
}

func readPrivate(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return nil, errors.New("invalid installer configuration file")
	}
	return io.ReadAll(io.LimitReader(f, 65537))
}
func (s *Service) platformURL() string {
	if s.cfg.Runtime.InstallerBaseURL != "" {
		return s.cfg.Runtime.InstallerBaseURL
	}
	return s.cfg.Server.BaseURL
}
func (s *Service) nodePayload(node config.RuntimeNode) (payload, error) {
	p := payload{NodeID: node.ID, Listen: node.InstallListen, PlatformURL: s.platformURL(), Manifest: s.manifest}
	files := []string{node.TokenFile, node.CAFile, node.InstallCertFile, node.InstallKeyFile}
	values := make([]string, 4)
	for i, path := range files {
		b, err := readPrivate(path)
		if err != nil {
			return p, errors.New("runtime installer node credential files unavailable")
		}
		values[i] = string(b)
	}
	p.Token = strings.TrimSpace(values[0])
	p.CA = values[1]
	p.Cert = values[2]
	p.Key = values[3]
	if p.Token == "" || strings.ContainsAny(p.Token, "\r\n") {
		return p, errors.New("invalid runtime installer node token")
	}
	pair, err := tls.X509KeyPair([]byte(p.Cert), []byte(p.Key))
	if err != nil || len(pair.Certificate) == 0 {
		return p, errors.New("invalid runtime installer TLS key pair")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return p, errors.New("invalid runtime installer certificate")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(p.CA)) {
		return p, errors.New("invalid runtime installer CA")
	}
	intermediates := x509.NewCertPool()
	for _, der := range pair.Certificate[1:] {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return p, errors.New("invalid runtime installer certificate chain")
		}
		intermediates.AddCert(cert)
	}
	u, err := url.Parse(node.URL)
	if err != nil {
		return p, errors.New("invalid runtime installer node URL")
	}
	if _, err = leaf.Verify(x509.VerifyOptions{DNSName: u.Hostname(), Roots: roots, Intermediates: intermediates}); err != nil {
		return p, errors.New("runtime installer certificate does not verify for the configured node URL")
	}
	if node.GuestImage != "" && node.GuestImage != s.manifest.GuestImage {
		return p, errors.New("runtime installer Guest image differs from the configured node")
	}
	return p, nil
}
func (s *Service) Script(ctx context.Context, token string) (string, error) {
	_, node, err := s.load(ctx, token, false)
	if err != nil {
		return "", err
	}
	p, err := s.nodePayload(node)
	if err != nil {
		return "", err
	}
	p.Ticket = token
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	tmpl, err := template.New("runtime_install").Parse(installTemplate)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	err = tmpl.Execute(&out, map[string]string{"payload": base64.StdEncoding.EncodeToString(b)})
	return out.String(), err
}
func (s *Service) Bundle(ctx context.Context, token string) (*os.File, error) {
	if _, _, err := s.load(ctx, token, false); err != nil {
		return nil, err
	}
	f, err := os.Open(s.archive)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !os.SameFile(info, s.archiveInfo) || info.Size() != s.archiveInfo.Size() || !info.ModTime().Equal(s.archiveInfo.ModTime()) {
		_ = f.Close()
		return nil, errors.New("runtime installer archive changed; reload configuration")
	}
	return f, nil
}
func (s *Service) Status(ctx context.Context, token, instance, fingerprint string) (bool, error) {
	t, node, err := s.load(ctx, token, true)
	if err != nil {
		return false, err
	}
	ready, err := s.confirmation.ConfirmNodeInstallation(ctx, node.ID, instance, fingerprint)
	if err != nil || !ready || t.Phase == "complete" {
		return ready, err
	}
	before, _ := json.Marshal(t)
	t.Phase = "complete"
	after, _ := json.Marshal(t)
	// Atomic and retry safe. Concurrent successful acknowledgements are allowed;
	// an expired or revoked ticket never becomes valid again.
	result, err := s.redis.Eval(ctx, `local v=redis.call('GET',KEYS[1]); if v==ARGV[1] then redis.call('SET',KEYS[1],ARGV[2],'KEEPTTL'); return 1 end; if v==ARGV[2] then return 1 end; return 0`, []string{ticketKey(token)}, string(before), string(after)).Int()
	if err != nil {
		return false, err
	}
	if result != 1 {
		return false, errcode.ErrRuntimeInstallToken
	}
	return true, nil
}
