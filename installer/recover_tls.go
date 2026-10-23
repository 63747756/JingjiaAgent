package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Called only on the private extracted backup. Never regenerate its CA or
// keys. Explicit replacement enterprise material is allowed on restore.
func prepareRecoveredTLS(s *deployment, o options) error {
	if (o.Certificate == "") != (o.Key == "") {
		return errors.New("--tls-cert and --tls-key must be supplied together")
	}
	if o.Certificate != "" {
		if err := validateTLS(s.PublicURL, o.Certificate, o.Key); err != nil {
			return fmt.Errorf("恢复企业证书校验失败: %w", err)
		}
		if err := copyFile(o.Certificate, filepath.Join(s.Root, "tls/server.crt"), 0600); err != nil {
			return err
		}
		if err := copyFile(o.Key, filepath.Join(s.Root, "tls/server.key"), 0600); err != nil {
			return err
		}
	}
	if o.CA != "" {
		ca, err := os.ReadFile(o.CA)
		if err != nil {
			return err
		}
		path := filepath.Join(s.Root, "tls/ca-bundle.crt")
		bundle, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err = privateWrite(path, append(append(bundle, '\n'), ca...)); err != nil {
			return err
		}
	}
	if validateTLSSet(s) == nil {
		return nil
	}
	certificates, err := renewedCertificates(s)
	if err != nil {
		return fmt.Errorf("备份证书无法恢复：%w；企业网站证书过期时请通过 --tls-cert、--tls-key 和必要的 --ca-cert 提供新证书", err)
	}
	for name, cert := range certificates {
		if err = privateWrite(filepath.Join(s.Root, "tls", name+".crt"), cert); err != nil {
			return err
		}
	}
	return validateTLSSet(s)
}
