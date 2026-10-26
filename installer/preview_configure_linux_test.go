//go:build linux

package main

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise command sequencing and rollback with a fake Docker CLI. HTTP/TLS
// health checks still use real local servers; VM acceptance covers containers.
func TestConfigurePreviewTransaction(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("installer maintenance requires root; run this test in the Linux test container")
	}
	for _, tc := range []struct {
		failure        string
		newCertificate bool
	}{{"", false}, {"nginx", false}, {"apply", false}, {"health", false}, {"apply", true}, {"health", true}} {
		name := tc.failure
		if tc.newCertificate {
			name += "-new-certificate"
		}
		t.Run(name, func(t *testing.T) {
			failure := tc.failure
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(root, "docker.log")
			marker := filepath.Join(root, "applied")
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$PREVIEW_COMMAND_LOG"
case "$*" in
  *"SELECT count(*) FROM runtime_commands"*) echo 0; exit 0;;
  *"SELECT DISTINCT public_port"*) exit 0;;
  run*) test "$PREVIEW_FAIL" != nginx; exit $?;;
  *"up -d"*) if [ "$PREVIEW_FAIL" = apply ] && [ ! -f "$PREVIEW_APPLIED" ]; then touch "$PREVIEW_APPLIED"; exit 1; fi; exit 0;;
  info*) if [ "$PREVIEW_FAIL" = health ]; then exit 1; fi; printf '%s\n' '{"OSType":"linux","Architecture":"amd64","ServerVersion":"28.0.0","MemoryLimit":true,"CPUCfsQuota":true}'; exit 0;;
esac
exit 0
`
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("PREVIEW_COMMAND_LOG", logPath)
			t.Setenv("PREVIEW_FAIL", failure)
			t.Setenv("PREVIEW_APPLIED", marker)
			api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"data":{"enabled":true,"initialized":true}}`))
			})
			public := httptest.NewServer(api)
			defer public.Close()
			internal := httptest.NewTLSServer(api)
			defer internal.Close()
			preview := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"service":"jingjiaagent-preview"}`))
			}))
			defer preview.Close()
			u, _ := url.Parse(preview.URL)
			s := previewTestDeployment(t)
			s.PublicURL = public.URL
			s.InternalURL = internal.URL
			s.PreviewURL = "http://127.0.0.1"
			s.PreviewPortRange = u.Port() + "-" + u.Port()
			certPath, keyPath := filepath.Join(root, "server.crt"), filepath.Join(root, "server.key")
			cert := internal.TLS.Certificates[0]
			if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600); err != nil {
				t.Fatal(err)
			}
			key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
				t.Fatal(err)
			}
			if err = prepareTLS(s, options{Certificate: certPath, Key: keyPath, CA: certPath}); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(s.Root, "tls", "operator-note.pem"), []byte("keep this unrelated TLS file\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = render(s); err != nil {
				t.Fatal(err)
			}
			s.Phase = "ready"
			if err = saveState(s); err != nil {
				t.Fatal(err)
			}
			before := map[string][]byte{}
			for _, file := range []string{"deployment.json", "compose.json", "gateway.conf", "config/server/config.yaml", "tls/server.crt", "tls/ca-bundle.crt", "tls/operator-note.pem", "secrets/ad.key"} {
				before[file], err = os.ReadFile(filepath.Join(s.Root, file))
				if err != nil {
					t.Fatal(err)
				}
			}
			opts := options{TrustedProxyCIDRs: "192.0.2.8/32", trustedProxySet: true}
			if tc.newCertificate {
				opts.PreviewURL, opts.previewURLSet = "https://127.0.0.1", true
				opts.PreviewCertificate, opts.PreviewKey, opts.CA = certPath, keyPath, certPath
			}
			err = configurePreview(s, opts)
			if (err == nil) != (failure == "") {
				t.Fatalf("configure failure=%s err=%v", failure, err)
			}
			commands, _ := os.ReadFile(logPath)
			if failure == "nginx" && strings.Contains(string(commands), "stop gateway backend") {
				t.Fatal("invalid Nginx configuration stopped running services")
			}
			if failure == "" {
				loaded, err := readState(s.Root)
				if err != nil || len(loaded.TrustedProxyCIDRs) != 1 {
					t.Fatal("new state not persisted", err)
				}
				if strings.Contains(string(mustReadTest(t, filepath.Join(s.Root, "compose.json"))), ".preview-config-") {
					t.Fatal("staging mount escaped into live Compose")
				}
			} else {
				for file, want := range before {
					if got := mustReadTest(t, filepath.Join(s.Root, file)); !bytes.Equal(got, want) {
						t.Fatal("rollback did not restore", file)
					}
				}
				if tc.newCertificate {
					for _, file := range []string{"preview.crt", "preview.key"} {
						if _, err := os.Lstat(filepath.Join(s.Root, "tls", file)); !os.IsNotExist(err) {
							t.Fatalf("rollback retained newly introduced %s: %v", file, err)
						}
					}
				}
			}
			if failure == "apply" || failure == "health" {
				if strings.Count(string(commands), "up -d --no-deps --force-recreate") != 2 {
					t.Fatal("original services not restarted after failure", string(commands))
				}
			}
		})
	}
}

func mustReadTest(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
