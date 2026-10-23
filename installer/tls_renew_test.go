package main

import (
	"bytes"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRenewalPreservesEnterpriseCertificateAndTrust(t *testing.T) {
	issuer := &deployment{Root: t.TempDir(), PublicURL: "https://agent.internal"}
	if err := prepareTLS(issuer, options{}); err != nil {
		t.Fatal(err)
	}
	for _, enterprise := range []bool{false, true} {
		t.Run(map[bool]string{false: "managed", true: "enterprise"}[enterprise], func(t *testing.T) {
			s := &deployment{Root: t.TempDir(), PublicURL: issuer.PublicURL}
			o := options{}
			if enterprise {
				o = options{Certificate: filepath.Join(issuer.Root, "tls/server.crt"), Key: filepath.Join(issuer.Root, "tls/server.key"), CA: filepath.Join(issuer.Root, "tls/ca.crt")}
			}
			if err := prepareTLS(s, o); err != nil {
				t.Fatal(err)
			}
			before := map[string][]byte{}
			for _, name := range []string{"ca.crt", "ca.key", "ca-bundle.crt", "server.crt", "server.key", "runtime.crt", "runtime.key"} {
				before[name], _ = os.ReadFile(filepath.Join(s.Root, "tls", name))
			}
			certs, err := renewedCertificates(s)
			if err != nil {
				t.Fatal(err)
			}
			if (certs["server"] == nil) != enterprise || bytes.Equal(certs["runtime"], before["runtime.crt"]) {
				t.Fatal("incorrect renewal selection")
			}
			for name, data := range before {
				current, _ := os.ReadFile(filepath.Join(s.Root, "tls", name))
				if !bytes.Equal(current, data) {
					t.Fatal("preparation changed live TLS", name)
				}
			}
			for name, data := range certs {
				block, _ := pem.Decode(data)
				cert, err := x509.ParseCertificate(block.Bytes)
				if err != nil {
					t.Fatal(err)
				}
				oldBlock, _ := pem.Decode(before[name+".crt"])
				old, _ := x509.ParseCertificate(oldBlock.Bytes)
				if cert.SerialNumber.Cmp(old.SerialNumber) == 0 || cert.NotAfter.Before(old.NotAfter) {
					t.Fatal("certificate was not renewed")
				}
				if err := privateWrite(filepath.Join(s.Root, "tls", name+".crt"), data); err != nil {
					t.Fatal(err)
				}
			}
			if err := validateTLSSet(s); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"ca.crt", "ca.key", "ca-bundle.crt", "server.key", "runtime.key"} {
				current, _ := os.ReadFile(filepath.Join(s.Root, "tls", name))
				if !bytes.Equal(current, before[name]) {
					t.Fatal("trust or key changed", name)
				}
			}
			if enterprise {
				current, _ := os.ReadFile(filepath.Join(s.Root, "tls/server.crt"))
				if !bytes.Equal(current, before["server.crt"]) {
					t.Fatal("enterprise certificate changed")
				}
			}
		})
	}
}

func TestRenewalInvalidKeyLeavesLiveSetUnchanged(t *testing.T) {
	s := &deployment{Root: t.TempDir(), PublicURL: "https://agent.internal"}
	if err := prepareTLS(s, options{}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(s.Root, "tls/runtime.crt"))
	if err := privateWrite(filepath.Join(s.Root, "tls/runtime.key"), []byte("broken")); err != nil {
		t.Fatal(err)
	}
	if _, err := renewedCertificates(s); err == nil {
		t.Fatal("invalid key accepted")
	}
	after, _ := os.ReadFile(filepath.Join(s.Root, "tls/runtime.crt"))
	if !bytes.Equal(before, after) {
		t.Fatal("live certificate changed")
	}
}

func TestRenewalCanReplaceExpiredInternalCertificate(t *testing.T) {
	s := &deployment{Root: t.TempDir(), PublicURL: "https://agent.internal"}
	if err := prepareTLS(s, options{}); err != nil {
		t.Fatal(err)
	}
	readCert := func(name string) *x509.Certificate {
		b, _ := os.ReadFile(filepath.Join(s.Root, "tls", name))
		p, _ := pem.Decode(b)
		c, err := x509.ParseCertificate(p.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	ca := readCert("ca.crt")
	runtime := readCert("runtime.crt")
	b, _ := os.ReadFile(filepath.Join(s.Root, "tls/ca.key"))
	block, _ := pem.Decode(b)
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	runtime.NotBefore = time.Now().Add(-48 * time.Hour)
	runtime.NotAfter = time.Now().Add(-24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, runtime, ca, runtime.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	privateWrite(filepath.Join(s.Root, "tls/runtime.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	if validateTLSSet(s) == nil {
		t.Fatal("fixture should be expired")
	}
	rootBefore, _ := os.ReadFile(filepath.Join(s.Root, "tls/ca.crt"))
	if err = prepareRecoveredTLS(s, options{}); err != nil {
		t.Fatal(err)
	}
	rootAfter, _ := os.ReadFile(filepath.Join(s.Root, "tls/ca.crt"))
	if !bytes.Equal(rootBefore, rootAfter) {
		t.Fatal("restore changed trust root")
	}
	if err = validateTLSSet(s); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryKeepsValidTLSAndRejectsBrokenMaterials(t *testing.T) {
	s := &deployment{Root: t.TempDir(), PublicURL: "https://agent.internal"}
	if err := prepareTLS(s, options{}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(s.Root, "tls/runtime.crt"))
	if err := prepareRecoveredTLS(s, options{}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(s.Root, "tls/runtime.crt"))
	if !bytes.Equal(before, after) {
		t.Fatal("unnecessary renewal")
	}
	privateWrite(filepath.Join(s.Root, "tls/runtime.key"), []byte("invalid"))
	if err := prepareRecoveredTLS(s, options{}); err == nil {
		t.Fatal("corrupt key accepted")
	}
	after, _ = os.ReadFile(filepath.Join(s.Root, "tls/runtime.crt"))
	if !bytes.Equal(before, after) {
		t.Fatal("partial renewal published")
	}
}

func TestRecoveryExpiredEnterpriseCertificateRequiresExplicitReplacement(t *testing.T) {
	issuer := &deployment{Root: t.TempDir(), PublicURL: "https://agent.internal"}
	if err := prepareTLS(issuer, options{}); err != nil {
		t.Fatal(err)
	}
	s := &deployment{Root: t.TempDir(), PublicURL: issuer.PublicURL}
	o := options{Certificate: filepath.Join(issuer.Root, "tls/server.crt"), Key: filepath.Join(issuer.Root, "tls/server.key"), CA: filepath.Join(issuer.Root, "tls/ca.crt")}
	if err := prepareTLS(s, o); err != nil {
		t.Fatal(err)
	}
	read := func(path string) []byte {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	rootBefore := read(filepath.Join(s.Root, "tls/ca.crt"))
	p, _ := pem.Decode(read(o.Certificate))
	leaf, _ := x509.ParseCertificate(p.Bytes)
	p, _ = pem.Decode(read(o.CA))
	ca, _ := x509.ParseCertificate(p.Bytes)
	p, _ = pem.Decode(read(filepath.Join(issuer.Root, "tls/ca.key")))
	key, _ := x509.ParsePKCS1PrivateKey(p.Bytes)
	leaf.NotBefore = time.Now().Add(-48 * time.Hour)
	leaf.NotAfter = time.Now().Add(-24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, leaf.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	expired := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateWrite(filepath.Join(s.Root, "tls/server.crt"), expired)
	if err = prepareRecoveredTLS(s, options{}); err == nil {
		t.Fatal("expired enterprise certificate silently replaced")
	}
	if !bytes.Equal(expired, read(filepath.Join(s.Root, "tls/server.crt"))) {
		t.Fatal("enterprise certificate mutated")
	}
	if err = prepareRecoveredTLS(s, o); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rootBefore, read(filepath.Join(s.Root, "tls/ca.crt"))) {
		t.Fatal("trust root replaced")
	}
	if !bytes.Equal(read(o.Certificate), read(filepath.Join(s.Root, "tls/server.crt"))) {
		t.Fatal("explicit replacement not used")
	}
}
