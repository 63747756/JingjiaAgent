package runtimeinstall

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestEnrollmentProofBindsCertificateTicketAndNode(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "node"}, DNSNames: []string{"node.internal"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, _ := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	text := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	cert, e := parseCertificate(text, "https://node.internal:7443")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = parseCertificate(text, "https://other.internal:7443"); e == nil {
		t.Fatal("wrong endpoint accepted")
	}
	msg := "jingjiaagent-enroll:ticket:node"
	hash := sha256.Sum256([]byte(msg))
	sig, _ := ecdsa.SignASN1(rand.Reader, key, hash[:])
	proof := base64.StdEncoding.EncodeToString(sig)
	if e = verifySignature(cert, msg, proof); e != nil {
		t.Fatal(e)
	}
	for _, other := range []string{"jingjiaagent-enroll:another:node", "jingjiaagent-enroll:ticket:other"} {
		if e = verifySignature(cert, other, proof); e == nil {
			t.Fatal("replayed proof accepted")
		}
	}
}
func TestLegacyInstallScriptStillCompilesWithRenewal(t *testing.T) {
	s, _, _, _ := installerFixture(t)
	token := issue(t, s)
	script, e := s.Script(context.Background(), token)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(script), "\r\n") {
		t.Fatal("Linux installer contains Windows line endings")
	}
}
