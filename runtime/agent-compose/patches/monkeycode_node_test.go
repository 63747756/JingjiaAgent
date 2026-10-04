package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestMonkeyCodeNodeIdentityPersistsAndRejectsCorruption(t *testing.T) {
	root := t.TempDir()
	first, err := monkeyCodeNodeIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := monkeyCodeNodeIdentity(root)
	if err != nil || second != first {
		t.Fatal("node identity changed across restart")
	}
	path := filepath.Join(root, "monkeycode-node-id")
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := monkeyCodeNodeIdentity(root); err == nil {
		t.Fatal("corrupt identity silently replaced")
	}
}

func TestMonkeyCodeNodeMetadataRequiresPrivateAuthAndActualDockerInfo(t *testing.T) {
	queries := 0
	docker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/_ping":
			w.Header().Set("API-Version", "1.45")
			_, _ = w.Write([]byte("OK"))
		case "/v1.45/info":
			queries++
			_, _ = w.Write([]byte(`{"ID":"fixture-docker","Name":"fixture-host","Architecture":"aarch64","OSType":"linux","NCPU":12,"MemTotal":34359738368}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer docker.Close()
	t.Setenv("DOCKER_HOST", docker.URL)
	t.Setenv("DOCKER_API_VERSION", "1.45")
	e := echo.New()
	RegisterMonkeyCodeNode(e, t.TempDir(), "private-test-token")
	request := func(token, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/internal/monkeycode/node", nil)
		r.Header.Set("Authorization", token)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		return w
	}
	for _, headers := range [][2]string{{"", ""}, {"Bearer wrong", ""}, {"Bearer private-test-token", "https://untrusted.example"}} {
		if got := request(headers[0], headers[1]); got.Code != 401 {
			t.Fatal("unauthorized node metadata exposed")
		}
	}
	if queries != 0 {
		t.Fatal("unauthorized request reached Docker")
	}
	w := request("Bearer private-test-token", "")
	var got monkeyCodeNodeSnapshot
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Cores != 12 || got.Memory != 32<<30 || got.Arch != "aarch64" || got.Hostname != "fixture-host" || got.InstanceID == "" || len(got.Fingerprint) != 64 || got.MemoryAvailable != nil {
		t.Fatalf("invalid node metadata: status %d", w.Code)
	}
	expectedPool := sha256.Sum256([]byte("fixture-docker"))
	if got.CapacityID != hex.EncodeToString(expectedPool[:]) {
		t.Fatal("capacity pool not derived from actual Docker engine")
	}
	other := echo.New()
	RegisterMonkeyCodeNode(other, t.TempDir(), "private-test-token")
	r2 := httptest.NewRequest(http.MethodGet, "/internal/monkeycode/node", nil)
	r2.Header.Set("Authorization", "Bearer private-test-token")
	w2 := httptest.NewRecorder()
	other.ServeHTTP(w2, r2)
	var alias monkeyCodeNodeSnapshot
	if json.Unmarshal(w2.Body.Bytes(), &alias) != nil || alias.InstanceID == got.InstanceID || alias.Fingerprint == got.Fingerprint || alias.CapacityID != got.CapacityID {
		t.Fatal("node aliases double-counted the same Docker engine")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("private node snapshot allowed caching")
	}
	// With remote Docker, this daemon's /proc must never be reported as the
	// remote machine's available memory. The persistent ID still remains stable.
	w = request("Bearer private-test-token", "")
	var again monkeyCodeNodeSnapshot
	if json.Unmarshal(w.Body.Bytes(), &again) != nil || again.InstanceID != got.InstanceID || again.Fingerprint != got.Fingerprint {
		t.Fatal("identity changed between observations")
	}
}
