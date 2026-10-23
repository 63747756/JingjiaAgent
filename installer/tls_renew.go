package main

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// Renew managed leaf certificates while retaining the installation trust root.
// Enterprise-issued server certificates must be renewed by their own CA.
func renewTLS(s *deployment) error {
	unlock, e := lock(s.Root)
	if e != nil {
		return e
	}
	defer unlock()
	certificates, e := renewedCertificates(s)
	if e != nil {
		return e
	}
	if e = stop(s); e != nil {
		return e
	}
	for _, name := range []string{"server", "runtime"} {
		if cert, ok := certificates[name]; ok {
			if e = privateWrite(filepath.Join(s.Root, "tls", name+".crt"), cert); e != nil {
				return e
			}
		}
	}
	if e = render(s); e != nil {
		return e
	}
	return startRendered(s)
}

// Prepare and validate the complete prospective set before stopping services.
// Reuse leaf keys so each atomic certificate replacement remains a valid pair,
// including when renewal is interrupted between server and runtime updates.
func renewedCertificates(s *deployment) (map[string][]byte, error) {
	certificates := map[string][]byte{}
	stage, e := os.MkdirTemp(s.Root, ".tls-renew-*")
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(stage) // Private temporary directory created above.
	staged := *s
	staged.Root = stage
	for _, name := range []string{"ca.crt", "ca.key", "ca-bundle.crt", "server.crt", "server.key", "runtime.crt", "runtime.key"} {
		if e = copyFile(filepath.Join(s.Root, "tls", name), filepath.Join(stage, "tls", name), 0600); e != nil {
			return nil, e
		}
	}
	raw, e := os.ReadFile(filepath.Join(s.Root, "tls/ca.crt"))
	if e != nil {
		return nil, e
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("invalid installation CA")
	}
	ca, e := x509.ParseCertificate(block.Bytes)
	if e != nil {
		return nil, e
	}
	raw, e = os.ReadFile(filepath.Join(s.Root, "tls/ca.key"))
	if e != nil {
		return nil, e
	}
	block, _ = pem.Decode(raw)
	if block == nil {
		return nil, errors.New("invalid installation CA key")
	}
	key, e := x509.ParsePKCS1PrivateKey(block.Bytes)
	if e != nil {
		return nil, e
	}
	raw, e = os.ReadFile(filepath.Join(s.Root, "tls/server.crt"))
	if e != nil {
		return nil, e
	}
	block, _ = pem.Decode(raw)
	if block == nil {
		return nil, errors.New("invalid server certificate")
	}
	server, e := x509.ParseCertificate(block.Bytes)
	if e != nil {
		return nil, e
	}
	managedServer := server.CheckSignatureFrom(ca) == nil
	if time.Now().AddDate(1, 0, 0).After(ca.NotAfter) {
		return nil, errors.New("CA 即将到期，需要显式迁移信任根")
	}
	u, _ := url.Parse(s.PublicURL)
	for _, name := range []string{"server", "runtime"} {
		if name == "server" && !managedServer {
			continue
		}
		pair, e := tls.LoadX509KeyPair(filepath.Join(s.Root, "tls", name+".crt"), filepath.Join(s.Root, "tls", name+".key"))
		if e != nil {
			return nil, e
		}
		leaf, e := x509.ParseCertificate(pair.Certificate[0])
		if e != nil {
			return nil, e
		}
		serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if e != nil {
			return nil, e
		}
		cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: u.Hostname()}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		if name == "runtime" {
			cert.DNSNames = []string{"runtime-proxy"}
		} else {
			if ip := net.ParseIP(u.Hostname()); ip != nil {
				cert.IPAddresses = []net.IP{ip}
			} else {
				cert.DNSNames = []string{u.Hostname()}
			}
			if s.PreviewURL != "" {
				p, _ := url.Parse(s.PreviewURL)
				cert.DNSNames = append(cert.DNSNames, p.Hostname(), "*."+p.Hostname())
			}
		}
		der, e := x509.CreateCertificate(rand.Reader, cert, ca, leaf.PublicKey, key)
		if e != nil {
			return nil, e
		}
		certificates[name] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		if e = privateWrite(filepath.Join(stage, "tls", name+".crt"), certificates[name]); e != nil {
			return nil, e
		}
	}
	if e = validateTLSSet(&staged); e != nil {
		return nil, e
	}
	return certificates, nil
}
