package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseSignatureAndArtifact(t *testing.T) {
	root := t.TempDir()
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	data := []byte("signed release bytes")
	hash := sha256.Sum256(data)
	m := manifest{Schema: 1, Product: "jingjiaagent", Version: "v1", Architecture: "amd64", DatabaseGeneration: 1, Files: map[string]artifact{"images.tar": {SHA256: hex.EncodeToString(hash[:]), Size: int64(len(data)), Online: true}}, Images: map[string]imageRecord{}}
	for _, name := range []string{"backend", "frontend", "daemon", "guest", "converter", "postgres", "redis", "storage", "nginx", "clickhouse"} {
		m.Images[name] = imageRecord{ID: "sha256:" + strings.Repeat("a", 64), Archive: "images.tar"}
	}
	raw, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(root, "manifest.json"), raw, 0600)
	os.WriteFile(filepath.Join(root, "manifest.sig"), []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, raw))), 0600)
	if _, e = loadManifest(root, base64.StdEncoding.EncodeToString(pub)); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(root, "images.tar")
	os.WriteFile(path, data, 0600)
	if e = verifyFile(path, m.Files["images.tar"]); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(path, []byte("different release!!"), 0600)
	if verifyFile(path, m.Files["images.tar"]) == nil {
		t.Fatal("tampered artifact accepted")
	}
	os.WriteFile(filepath.Join(root, "manifest.json"), append(raw, ' '), 0600)
	if _, e = loadManifest(root, base64.StdEncoding.EncodeToString(pub)); e == nil {
		t.Fatal("tampered manifest accepted")
	}
}
func TestRejectTraversal(t *testing.T) {
	for _, path := range []string{"../secrets", "a/../secret", "/etc/passwd", "x\\y", "C:/secret", "./x"} {
		if _, e := contained(t.TempDir(), path); e == nil {
			t.Errorf("accepted %q", path)
		}
	}
}
func TestRenderPreservesSecretsAndCustomConfiguration(t *testing.T) {
	root := t.TempDir()
	s := &deployment{PreviewSubnet: "172.30.240.0/29", Schema: 1, Root: root, Data: filepath.Join(t.TempDir(), "data"), PublicURL: "https://agent.internal:8443", Mode: "single", Project: "deploy-test", Version: "test", Images: map[string]imageRecord{}}
	for _, name := range []string{"backend", "frontend", "daemon", "guest", "converter", "postgres", "redis", "storage", "nginx", "clickhouse"} {
		s.Images[name] = imageRecord{ID: "sha256:" + strings.Repeat("a", 64)}
	}
	if e := prepareTLS(s, options{}); e != nil {
		t.Fatal(e)
	}
	certPEM, _ := os.ReadFile(filepath.Join(root, "tls/server.crt"))
	block, _ := pem.Decode(certPEM)
	cert, e := x509.ParseCertificate(block.Bytes)
	if e != nil || len(cert.AuthorityKeyId) == 0 {
		t.Fatal("strict TLS clients require AuthorityKeyId", e)
	}
	if e := render(s); e != nil {
		t.Fatal(e)
	}
	secret, e := os.ReadFile(filepath.Join(root, "secrets/ad.key"))
	if e != nil {
		t.Fatal(e)
	}
	cfgPath := filepath.Join(root, "config/server/config.yaml")
	raw, _ := os.ReadFile(cfgPath)
	var cfg object
	json.Unmarshal(raw, &cfg)
	cfg["work"].(map[string]any)["skillhub"] = object{"url": "https://hub.internal"}
	writeJSON(cfgPath, cfg)
	if e = render(s); e != nil {
		t.Fatal(e)
	}
	again, _ := os.ReadFile(filepath.Join(root, "secrets/ad.key"))
	if string(secret) != string(again) {
		t.Fatal("encryption key rotated")
	}
	raw, _ = os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "hub.internal") {
		t.Fatal("custom settings lost")
	}
	raw, _ = os.ReadFile(filepath.Join(root, "compose.json"))
	var compose object
	json.Unmarshal(raw, &compose)
	services := compose["services"].(map[string]any)
	if _, ok := services["offline-egress"]; ok {
		t.Fatal("obsolete proxy present")
	}
	backend := services["backend"].(map[string]any)
	if _, ok := backend["environment"].(map[string]any)["HTTP_PROXY"]; ok {
		t.Fatal("forced proxy present")
	}
	networks := compose["networks"].(map[string]any)
	if networks["sandbox"].(map[string]any)["internal"] == true {
		t.Fatal("task egress blocked")
	}
	if networks["business"].(map[string]any)["internal"] != true || networks["conversion"].(map[string]any)["internal"] != true {
		t.Fatal("data isolation lost")
	}
	raw, _ = os.ReadFile(filepath.Join(root, "gateway.conf"))
	if !strings.Contains(string(raw), "listen 8443 ssl") {
		t.Fatal("public port not exposed inside network")
	}
	if !strings.Contains(string(raw), "location /v1/") {
		t.Fatal("model proxy is not exposed")
	}
	if e = checkSecrets(s); e != nil {
		t.Fatal(e)
	}
	os.Remove(filepath.Join(root, "secrets/payload.key"))
	if checkSecrets(s) == nil {
		t.Fatal("missing persistent key accepted")
	}
	if render(s) == nil {
		t.Fatal("render regenerated a lost persistent key")
	}
}
