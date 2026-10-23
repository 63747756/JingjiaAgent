package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Nothing under tls/ is published until the complete set has been validated.
// Incomplete materials from an interrupted first install have never been used
// by services; retain them for diagnosis while replacing the entire directory.
func prepareTLS(s *deployment, o options) error {
	if (o.Certificate == "") != (o.Key == "") {
		return errors.New("--tls-cert and --tls-key must be supplied together")
	}
	if o.Certificate != "" {
		if err := validateServerTLS(s, o.Certificate, o.Key); err != nil {
			return fmt.Errorf("企业证书校验失败: %w", err)
		}
	}
	if o.CA != "" {
		b, err := os.ReadFile(o.CA)
		if err != nil {
			return err
		}
		if !x509.NewCertPool().AppendCertsFromPEM(b) {
			return errors.New("invalid enterprise CA")
		}
	}
	if err := validateTLSSet(s); err == nil {
		return nil
	}
	_, err := os.Stat(filepath.Join(s.Root, "config/server/config.yaml"))
	if err == nil || !os.IsNotExist(err) || (s.Phase != "" && s.Phase != "preparing") {
		return errors.New("已有部署的证书材料不完整或无效，请恢复证书备份或续签；不会重建信任根")
	}
	if err = os.MkdirAll(s.Root, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(s.Root, ".tls-prepare-*")
	if err != nil {
		return err
	}
	staged := *s
	staged.Root = stage
	if err = generateTLS(&staged, o); err != nil {
		return err
	}
	if err = validateTLSSet(&staged); err != nil {
		return err
	}
	target := filepath.Join(s.Root, "tls")
	previous := filepath.Join(stage, "previous")
	hadPrevious := false
	if info, e := os.Lstat(target); e == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("TLS directory must not be a symlink or file")
		}
		if err = os.Rename(target, previous); err != nil {
			return err
		}
		hadPrevious = true
	} else if !os.IsNotExist(e) {
		return e
	}
	if err = os.Rename(filepath.Join(stage, "tls"), target); err != nil {
		if hadPrevious {
			_ = os.Rename(previous, target)
		}
		return err
	}
	if !hadPrevious {
		_ = os.Remove(stage) // Empty staging directory only.
	}
	return nil
}

func validateTLSSet(s *deployment) error {
	dir := filepath.Join(s.Root, "tls")
	if err := validateServerTLS(s, filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")); err != nil {
		return err
	}
	if err := validateTLS("https://runtime-proxy", filepath.Join(dir, "runtime.crt"), filepath.Join(dir, "runtime.key")); err != nil {
		return err
	}
	caPEM, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		return err
	}
	key, err := os.ReadFile(filepath.Join(dir, "ca.key"))
	if err != nil {
		return err
	}
	if _, err = tls.X509KeyPair(caPEM, key); err != nil {
		return err
	}
	bundle, err := os.ReadFile(filepath.Join(dir, "ca-bundle.crt"))
	if err != nil || !bytes.Contains(bundle, caPEM) {
		return errors.New("installation CA bundle is incomplete")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(bundle) {
		return errors.New("invalid installation CA bundle")
	}
	for _, name := range []string{"server", "runtime"} {
		raw, err := os.ReadFile(filepath.Join(dir, name+".crt"))
		if err != nil {
			return err
		}
		leaf, rest := pem.Decode(raw)
		if leaf == nil {
			return errors.New("invalid TLS leaf")
		}
		cert, err := x509.ParseCertificate(leaf.Bytes)
		if err != nil {
			return err
		}
		intermediates := x509.NewCertPool()
		intermediates.AppendCertsFromPEM(rest)
		if _, err = cert.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates}); err != nil {
			return err
		}
	}
	return nil
}
