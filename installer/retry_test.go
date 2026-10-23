package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRetryRejectsChangedDeploymentIdentity(t *testing.T) {
	s := &deployment{PublicURL: "https://agent.internal", PreviewURL: "https://preview.internal", Mode: "single", Data: "/var/lib/custom", Project: "custom-project"}
	original := options{PublicURL: s.PublicURL, PreviewURL: s.PreviewURL, Mode: s.Mode, Data: s.Data, Project: s.Project}
	if err := validateRetryOptions(s, original); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*options){
		func(o *options) { o.Project = "jingjiaagent" },
		func(o *options) { o.PreviewURL = "" },
		func(o *options) { o.Data = "/var/lib/other" },
	} {
		o := original
		change(&o)
		if validateRetryOptions(s, o) == nil {
			t.Fatal("changed deployment identity accepted")
		}
	}
}

func TestInvalidTLSInputsDoNotPublishPartialCertificates(t *testing.T) {
	s := &deployment{Root: t.TempDir(), PublicURL: "https://agent.internal", Phase: "preparing"}
	for _, o := range []options{{Certificate: "missing.crt"}, {Certificate: "missing.crt", Key: "missing.key"}, {CA: "missing.pem"}} {
		if prepareTLS(s, o) == nil {
			t.Fatal("invalid certificate options accepted")
		}
		if _, err := os.Stat(filepath.Join(s.Root, "tls")); !os.IsNotExist(err) {
			t.Fatal("invalid input published TLS state")
		}
	}
	if err := prepareTLS(s, options{}); err != nil {
		t.Fatal(err)
	}
	if err := validateTLSSet(s); err != nil {
		t.Fatal(err)
	}
	ca, _ := os.ReadFile(filepath.Join(s.Root, "tls/ca.crt"))
	if err := prepareTLS(s, options{}); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(filepath.Join(s.Root, "tls/ca.crt"))
	if !bytes.Equal(ca, again) {
		t.Fatal("retry rotated the trust root")
	}
}

func TestInterruptedFirstTLSPreparationCanBeRetried(t *testing.T) {
	s := &deployment{Root: t.TempDir(), PublicURL: "https://agent.internal", Phase: "preparing"}
	// The old installer could publish this prefix before failing on TLS options.
	if err := generateTLS(s, options{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(s.Root, "tls/ca.crt")); err != nil {
		t.Fatal(err)
	}
	if err := prepareTLS(s, options{}); err != nil {
		t.Fatal(err)
	}
	if err := validateTLSSet(s); err != nil {
		t.Fatal(err)
	}
	// Once services have configuration, incomplete secrets cannot be regenerated.
	if err := privateWrite(filepath.Join(s.Root, "config/server/config.yaml"), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	oldKey, _ := os.ReadFile(filepath.Join(s.Root, "tls/ca.key"))
	if err := os.Remove(filepath.Join(s.Root, "tls/runtime.key")); err != nil {
		t.Fatal(err)
	}
	if prepareTLS(s, options{}) == nil {
		t.Fatal("running deployment silently regenerated certificates")
	}
	newKey, _ := os.ReadFile(filepath.Join(s.Root, "tls/ca.key"))
	if !bytes.Equal(oldKey, newKey) {
		t.Fatal("published trust root changed")
	}
}

func TestEnterpriseCertificatesAreValidatedAsACompleteSet(t *testing.T) {
	issuer := &deployment{Root: t.TempDir(), PublicURL: "https://agent.internal"}
	if err := prepareTLS(issuer, options{}); err != nil {
		t.Fatal(err)
	}
	s := &deployment{Root: t.TempDir(), PublicURL: issuer.PublicURL}
	o := options{Certificate: filepath.Join(issuer.Root, "tls/server.crt"), Key: filepath.Join(issuer.Root, "tls/server.key"), CA: filepath.Join(issuer.Root, "tls/ca.crt")}
	if err := prepareTLS(s, o); err != nil {
		t.Fatal(err)
	}
	if err := validateTLSSet(s); err != nil {
		t.Fatal(err)
	}
}
