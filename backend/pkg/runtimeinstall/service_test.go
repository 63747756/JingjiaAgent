package runtimeinstall

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/errcode"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type installAuth struct {
	revoked bool
	mu      sync.Mutex
}

func (a *installAuth) AuthorizeRuntimeInstall(_ context.Context, actor, team string, n config.RuntimeNode) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.revoked || actor != n.OwnerID || team != n.TeamID {
		return errcode.ErrRuntimeInstallScope
	}
	return nil
}

type installConfirmation struct{ ready bool }

func (a *installConfirmation) ConfirmNodeInstallation(_ context.Context, node, instance, fingerprint string) (bool, error) {
	if instance != "installed-instance" || fingerprint != "installed-fingerprint" {
		return false, errcode.ErrRuntimeInstallIdentity
	}
	return a.ready, nil
}
func installerFixture(t *testing.T) (*Service, *miniredis.Miniredis, *installAuth, *installConfirmation) {
	t.Helper()
	dir := t.TempDir()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "runtime.invalid"}, DNSNames: []string{"runtime.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, _ := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	keyder, _ := x509.MarshalECPrivateKey(key)
	files := map[string][]byte{"token": []byte("node-only-private-token"), "ca": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), "cert": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), "key": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyder}), "images.tar": []byte("offline-image-bundle")}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(dir, name), value, 0600); err != nil {
			t.Fatal(err)
		}
	}
	hash := sha256.Sum256(files["images.tar"])
	image := "sha256:" + strings.Repeat("a", 64)
	m := Manifest{Schema: 1, Architecture: "amd64", Archive: "images.tar", SHA256: hex.EncodeToString(hash[:]), DaemonImage: image, GuestImage: image, ProxyImage: image}
	b, _ := json.Marshal(m)
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0600)
	cfg := &config.Config{}
	cfg.Server.BaseURL = "https://platform.invalid"
	cfg.Runtime = config.Runtime{Backend: "agent_compose", InstallerManifestFile: filepath.Join(dir, "manifest.json"), Nodes: []config.RuntimeNode{{ID: uuid.NewString(), OwnerID: uuid.NewString(), TeamID: uuid.NewString(), URL: "https://runtime.invalid", TokenFile: filepath.Join(dir, "token"), CAFile: filepath.Join(dir, "ca"), InstallCertFile: filepath.Join(dir, "cert"), InstallKeyFile: filepath.Join(dir, "key"), Install: true, InstallListen: "0.0.0.0:7443"}}}
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { client.Close() })
	auth := &installAuth{}
	confirmation := &installConfirmation{}
	s, err := New(cfg, client, auth, confirmation)
	if err != nil {
		t.Fatal(err)
	}
	return s, redisServer, auth, confirmation
}
func issue(t *testing.T, s *Service) string {
	t.Helper()
	node := s.cfg.Runtime.Nodes[0]
	command, err := s.Command(context.Background(), node.OwnerID, node.TeamID)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(command, "https://")
	end := strings.Index(command[start:], "'")
	u, err := url.Parse(command[start : start+end])
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("token")
}
func TestInstallTicketAuthorizationExpiryAndCompletion(t *testing.T) {
	s, r, auth, confirmation := installerFixture(t)
	ctx := context.Background()
	token := issue(t, s)
	script, err := s.Script(ctx, token)
	if err != nil || !strings.Contains(script, "MONKEYCODE_INSTALL") {
		t.Fatal("authorized installer script unavailable", err)
	}
	if _, err = s.Command(ctx, uuid.NewString(), s.cfg.Runtime.Nodes[0].TeamID); err == nil {
		t.Fatal("other actor received node credentials")
	}
	if _, err = s.Script(ctx, uuid.NewString()); err == nil {
		t.Fatal("unknown ticket accepted")
	}
	auth.revoked = true
	if _, err = s.Script(ctx, token); err == nil {
		t.Fatal("revoked permission allowed script")
	}
	if _, err = s.Bundle(ctx, token); err == nil {
		t.Fatal("revoked permission allowed bundle")
	}
	if _, err = s.Status(ctx, token, "installed-instance", "installed-fingerprint"); err == nil {
		t.Fatal("revoked permission allowed confirmation")
	}
	auth.revoked = false
	if ready, err := s.Status(ctx, token, "installed-instance", "installed-fingerprint"); ready || err != nil {
		t.Fatal("offline node confirmed")
	}
	confirmation.ready = true
	if _, err := s.Status(ctx, token, "different-machine", "installed-fingerprint"); err == nil {
		t.Fatal("wrong installation identity confirmed")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ready, err := s.Status(ctx, token, "installed-instance", "installed-fingerprint"); !ready || err != nil {
				t.Error("concurrent confirmation not idempotent", err)
			}
		}()
	}
	wg.Wait()
	if _, err = s.Script(ctx, token); err == nil {
		t.Fatal("completed ticket disclosed script credentials")
	}
	if f, err := s.Bundle(ctx, token); err == nil {
		f.Close()
		t.Fatal("completed ticket downloaded bundle")
	}
	token = issue(t, s)
	r.FastForward(2 * time.Hour)
	if _, err = s.Script(ctx, token); err == nil {
		t.Fatal("expired ticket accepted")
	}
}

func TestPersonalNodeTicketCannotBeUsedByOtherAccountsOrTeams(t *testing.T) {
	s, _, _, _ := installerFixture(t)
	ctx := context.Background()
	s.cfg.Runtime.Nodes[0].TeamID = ""
	token := issue(t, s)
	if _, err := s.Script(ctx, token); err != nil {
		t.Fatal("personal owner could not obtain installation script", err)
	}
	if _, err := s.Command(ctx, uuid.NewString(), ""); err == nil {
		t.Fatal("other personal account received node credentials")
	}
	if _, err := s.Command(ctx, s.cfg.Runtime.Nodes[0].OwnerID, uuid.NewString()); err == nil {
		t.Fatal("team context selected a personal node")
	}
}
func TestInstallerRejectsChangedArchiveAndInvalidTLS(t *testing.T) {
	s, _, _, _ := installerFixture(t)
	token := issue(t, s)
	ctx := context.Background()
	if f, err := s.Bundle(ctx, token); err != nil {
		t.Fatal(err)
	} else {
		f.Close()
	}
	_ = os.WriteFile(s.archive, []byte("tampered archive"), 0600)
	if f, err := s.Bundle(ctx, token); err == nil {
		f.Close()
		t.Fatal("changed archive accepted")
	}
	if _, err := New(s.cfg, s.redis, s.auth, s.confirmation); err == nil {
		t.Fatal("startup accepted checksum mismatch")
	}
	s, _, _, _ = installerFixture(t)
	s.cfg.Runtime.Nodes[0].URL = "https://wrong-name.invalid"
	if _, err := New(s.cfg, s.redis, s.auth, s.confirmation); err == nil {
		t.Fatal("certificate hostname mismatch accepted")
	}
}

func TestInstallerBootstrapPropagatesDownloadFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux installer")
	}
	s, _, _, _ := installerFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "untrusted error body", http.StatusForbidden)
	}))
	defer server.Close()
	s.cfg.Server.BaseURL = server.URL
	n := s.cfg.Runtime.Nodes[0]
	command, err := s.Command(context.Background(), n.OwnerID, n.TeamID)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("bash", "-c", command).CombinedOutput(); err == nil {
		t.Fatal("failed script download reported installation success")
	} else if strings.Contains(string(output), "untrusted error body") {
		t.Fatal("server error body executed or exposed")
	}
	s.cfg.Server.BaseURL = "http://public.example"
	if _, err := New(s.cfg, s.redis, s.auth, s.confirmation); err == nil {
		t.Fatal("private installer allowed insecure non-local platform")
	}
}
