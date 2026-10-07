package secretbox

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func keyFile(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "secret.key")
	if err := os.WriteFile(path, key, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRoundtripNonceAndRestart(t *testing.T) {
	path := keyFile(t)
	box, err := NewFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"", "query password", "  密码\\with spaces\n  "} {
		first, err := box.Seal(plain)
		if err != nil {
			t.Fatal(err)
		}
		second, err := box.Seal(plain)
		if err != nil {
			t.Fatal(err)
		}
		if first == second {
			t.Fatal("nonce was reused")
		}
		restarted, err := NewFromFile(path)
		if err != nil {
			t.Fatal(err)
		}
		opened, err := restarted.Open(first)
		if err != nil || opened != plain {
			t.Fatalf("roundtrip failed: %v", err)
		}
		if plain != "" && strings.Contains(first, plain) {
			t.Fatal("plaintext exposed")
		}
	}
}

func TestWrongKeyTamperingAndInvalidVersion(t *testing.T) {
	box, err := NewFromFile(keyFile(t))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal("secret")
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewFromFile(keyFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(sealed); !errors.Is(err, ErrCiphertext) {
		t.Fatal(err)
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sealed, prefix))
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	for _, value := range []string{"", "plaintext secret", "v2:" + strings.TrimPrefix(sealed, prefix), "v1:!!", "v1:" + base64.RawURLEncoding.EncodeToString(data), "v1:AA"} {
		if _, err := box.Open(value); !errors.Is(err, ErrCiphertext) {
			t.Fatalf("value accepted: %v", err)
		}
	}
}

func TestMissingAndInvalidKeyIsNotGenerated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.key")
	if _, err := NewFromFile(path); !errors.Is(err, ErrKey) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing key was generated")
	}
	for _, size := range []int{0, 31, 33, 1024} {
		if err := os.WriteFile(path, make([]byte, size), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFromFile(path); !errors.Is(err, ErrKey) {
			t.Fatalf("accepted key size %d: %v", size, err)
		}
	}
	if _, err := NewFromFile(""); !errors.Is(err, ErrKey) {
		t.Fatal(err)
	}
	if _, err := NewFromFile(t.TempDir()); !errors.Is(err, ErrKey) {
		t.Fatal(err)
	}
}

func TestPrivateKeyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ACLs are managed by deployment, not Unix permission bits")
	}
	path := keyFile(t)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFromFile(path); !errors.Is(err, ErrKey) {
		t.Fatal("world-readable key accepted")
	}
}

func TestUninitializedBox(t *testing.T) {
	for _, box := range []*Box{nil, {}} {
		if _, err := box.Seal("secret"); !errors.Is(err, ErrUninitialized) {
			t.Fatal(err)
		}
		if _, err := box.Open("v1:AA"); !errors.Is(err, ErrUninitialized) {
			t.Fatal(err)
		}
	}
}
