package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type artifact struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Online bool   `json:"online"`
}
type imageRecord struct {
	Ref      string `json:"ref"`
	ID       string `json:"id"`
	ConfigID string `json:"config_id"`
	LocalTag string `json:"local_tag"`
	Archive  string `json:"archive"`
}
type manifest struct {
	Sequence           int64                  `json:"sequence"`
	Schema             int                    `json:"schema"`
	Product            string                 `json:"product"`
	Version            string                 `json:"version"`
	Architecture       string                 `json:"architecture"`
	DatabaseGeneration int                    `json:"database_generation"`
	Images             map[string]imageRecord `json:"images"`
	Files              map[string]artifact    `json:"files"`
}

var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$`)
var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func contained(root, name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00:") || filepath.IsAbs(name) {
		return "", errors.New("invalid release path")
	}
	clean := filepath.Clean(name)
	if filepath.ToSlash(clean) != name || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("release path escapes its directory")
	}
	path := filepath.Join(root, clean)
	for p := path; p != root; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err == nil && st.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("release paths must not contain symlinks")
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	return path, nil
}

func loadManifest(root, publicKey string) (*manifest, error) {
	key, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("installer has no valid release verification key; build it through the signed release pipeline")
	}
	p, err := contained(root, "manifest.json")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if len(data) > 2<<20 {
		return nil, errors.New("release manifest too large")
	}
	p, err = contained(root, "manifest.sig")
	if err != nil {
		return nil, err
	}
	signature, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil || !ed25519.Verify(key, data, sig) {
		return nil, errors.New("release signature verification failed")
	}
	var m manifest
	if err = json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m.Schema != 1 || m.Product != "jingjiaagent" || m.Architecture != "amd64" || !safeName.MatchString(m.Version) || m.DatabaseGeneration < 1 {
		return nil, errors.New("unsupported release manifest")
	}
	for name, file := range m.Files {
		if _, err = contained(root, name); err != nil {
			return nil, err
		}
		if file.Size < 0 || file.Size > 64<<30 || !shaPattern.MatchString(file.SHA256) {
			return nil, errors.New("invalid release artifact identity")
		}
	}
	for _, name := range []string{"backend", "frontend", "daemon", "guest", "converter", "postgres", "redis", "storage", "nginx", "clickhouse"} {
		im, ok := m.Images[name]
		if !ok || !strings.HasPrefix(im.ID, "sha256:") || !shaPattern.MatchString(strings.TrimPrefix(im.ID, "sha256:")) {
			return nil, fmt.Errorf("missing immutable image: %s", name)
		}
		if im.Ref != "" && (!strings.Contains(im.Ref, "@sha256:") || strings.ContainsAny(im.Ref, "\n\r\t ")) {
			return nil, fmt.Errorf("online image must use registry digest: %s", name)
		}
		if _, ok = m.Files[im.Archive]; !ok {
			return nil, fmt.Errorf("missing image archive: %s", name)
		}
	}
	return &m, nil
}

func verifyFile(path string, a artifact) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Size() != a.Size {
		return errors.New("artifact size mismatch: " + filepath.Base(path))
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return errors.New("artifact checksum mismatch: " + filepath.Base(path))
	}
	return nil
}

func httpsURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("release URL must use HTTPS without credentials, query or fragment")
	}
	return u, nil
}

func download(raw, path string, expected *artifact) error {
	if _, err := httpsURL(raw); err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 4 || req.URL.Scheme != "https" || req.URL.Host != via[0].URL.Host {
			return errors.New("release redirect changes trust boundary")
		}
		return nil
	}}
	resp, err := client.Get(raw)
	if err != nil {
		return errors.New("cannot download release artifact; check HTTPS connectivity")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("artifact download returned HTTP %d", resp.StatusCode)
	}
	limit := int64(2 << 20)
	if expected != nil {
		limit = expected.Size
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".download-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	n, err := io.Copy(f, io.LimitReader(resp.Body, limit+1))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n > limit {
		return errors.New("download exceeds manifest size")
	}
	if expected != nil {
		if err = verifyFile(tmp, *expected); err != nil {
			return err
		}
	}
	return os.Rename(tmp, path)
}

func acquire(o options) (string, *manifest, error) {
	root := o.Bundle
	if o.Offline && root == "" {
		return "", nil, errors.New("offline mode requires --bundle")
	}
	if root == "" {
		if _, err := httpsURL(o.ReleaseURL); err != nil {
			return "", nil, err
		}
		root = filepath.Join(o.Root, "downloads")
		for _, name := range []string{"manifest.json", "manifest.sig"} {
			if err := download(strings.TrimRight(o.ReleaseURL, "/")+"/"+name, filepath.Join(root, name), nil); err != nil {
				return "", nil, err
			}
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", nil, err
	}
	m, err := loadManifest(root, releasePublicKey)
	if err != nil {
		return "", nil, err
	}
	for name, a := range m.Files {
		if !o.Offline && !a.Online {
			continue
		}
		p, err := contained(root, name)
		if err != nil {
			return "", nil, err
		}
		if err = verifyFile(p, a); err == nil {
			continue
		}
		if o.Offline || o.ReleaseURL == "" {
			return "", nil, fmt.Errorf("required artifact %s: %w", name, err)
		}
		if err = download(strings.TrimRight(o.ReleaseURL, "/")+"/"+name, p, &a); err != nil {
			return "", nil, err
		}
	}
	return root, m, nil
}
