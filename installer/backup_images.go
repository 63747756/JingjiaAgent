package main

import (
	"archive/tar"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type backupImage struct {
	ID         string    `json:"id"`
	ConfigID   string    `json:"config_id,omitempty"`
	Archive    *artifact `json:"archive,omitempty"`
	References []string  `json:"references"`
	Release    bool      `json:"release"`
}
type backupImageManifest struct {
	Schema  int           `json:"schema"`
	Images  []backupImage `json:"images"`
	Archive *artifact     `json:"archive,omitempty"`
}

func inspectImage(ref string) (string, error) {
	if ref == "" || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\r\n\x00 ") {
		return "", errors.New("invalid image reference")
	}
	b, err := output("docker", "image", "inspect", ref)
	if err != nil {
		return "", fmt.Errorf("所需镜像在本机不存在: %s", ref)
	}
	var rows []struct {
		ID               string `json:"Id"`
		Os, Architecture string
	}
	if json.Unmarshal(b, &rows) != nil || len(rows) != 1 || rows[0].Os != "linux" || rows[0].Architecture != "amd64" || !validImageID(rows[0].ID) {
		return "", errors.New("invalid backup image identity")
	}
	return rows[0].ID, nil
}
func validImageID(id string) bool {
	return strings.HasPrefix(id, "sha256:") && shaPattern.MatchString(strings.TrimPrefix(id, "sha256:"))
}

func sandboxImageReferences(data string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(data, "runtime/sandboxes/*/*/*/*/vm/runtime.json"))
	if err != nil {
		return nil, err
	}
	refs := []string{}
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var vm struct{ Image, Driver string }
		if json.Unmarshal(b, &vm) != nil || vm.Driver != "docker" || vm.Image == "" {
			return nil, fmt.Errorf("工作空间镜像记录无效: %s", file)
		}
		refs = append(refs, vm.Image)
	}
	return refs, nil
}

// Read the catalog with only PostgreSQL running, after producers and guests
// have stopped. Close it again before copying its data files into the backup.
func backupCatalogReferences(s *deployment) (optional, required []string, err error) {
	if _, err = os.Stat(filepath.Join(s.Data, "postgres/PG_VERSION")); err != nil {
		return nil, nil, err
	}
	running, err := runningServices(s)
	if err != nil {
		return nil, nil, err
	}
	if len(running) != 0 {
		return nil, nil, errors.New("镜像备份需要先停止全部服务")
	}
	defer func() { err = errors.Join(err, compose(s, "stop", "postgres")) }()
	if err = compose(s, "up", "-d", "--no-deps", "--wait", "--wait-timeout", "120", "postgres"); err != nil {
		return nil, nil, err
	}
	b, err := deploymentSQL(s, `SELECT json_build_object('images',COALESCE((SELECT json_agg(name) FROM images),'[]'::json),'nodes',COALESCE((SELECT json_agg(json_build_object('id',node_id,'payload',encode(payload,'hex'))) FROM runtime_node_configs WHERE endpoint='https://runtime-proxy:7411'),'[]'::json))`)
	if err != nil {
		return nil, nil, err
	}
	var catalog struct {
		Images []string
		Nodes  []struct{ ID, Payload string }
	}
	if err = json.Unmarshal(b, &catalog); err != nil {
		return nil, nil, err
	}
	optional = catalog.Images
	if len(catalog.Nodes) == 0 {
		return optional, nil, nil
	}
	key, err := os.ReadFile(filepath.Join(s.Root, "secrets/payload.key"))
	if err != nil {
		return nil, nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	for _, node := range catalog.Nodes {
		data, e := hex.DecodeString(node.Payload)
		if e != nil || len(data) < aead.NonceSize() {
			return nil, nil, errors.New("invalid local node payload")
		}
		plain, e := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte("runtime-node:"+node.ID))
		if e != nil {
			return nil, nil, errors.New("cannot decrypt local node image configuration")
		}
		var value struct {
			Config struct {
				Guest     string            `json:"guest_image"`
				Installed string            `json:"installed_guest_image"`
				Recovered map[string]string `json:"recovered_images"`
			}
		}
		if e = json.Unmarshal(plain, &value); e != nil {
			return nil, nil, e
		}
		ref := value.Config.Installed
		if ref == "" {
			ref = value.Config.Guest
		}
		if mapped := value.Config.Recovered[ref]; mapped != "" {
			ref = mapped
		}
		if ref != "" {
			required = append(required, ref)
		}
		for _, ref := range value.Config.Recovered {
			required = append(required, ref)
		}
	}
	return optional, required, nil
}

func prepareBackupImages(s *deployment, dir string) error {
	optional, required, err := backupCatalogReferences(s)
	if err != nil {
		return err
	}
	refs, err := sandboxImageReferences(s.Data)
	if err != nil {
		return err
	}
	required = append(required, refs...)
	if guest := s.Images["guest"].ID; guest != "" {
		required = append(required, guest)
	}
	return saveBackupImages(s, dir, optional, required)
}

func saveBackupImages(s *deployment, dir string, optional, required []string) error {
	refs := map[string]bool{}
	for _, ref := range optional {
		refs[ref] = false
	}
	for _, ref := range required {
		refs[ref] = true
	}
	byID := map[string]*backupImage{}
	for ref, mustExist := range refs {
		id, err := inspectImage(ref)
		if err != nil {
			if mustExist {
				return err
			}
			continue // Images installed only on remote hosts are backed up there.
		}
		if byID[id] == nil {
			byID[id] = &backupImage{ID: id}
		}
		byID[id].References = append(byID[id].References, ref)
	}
	m := backupImageManifest{Schema: 2, Images: []backupImage{}}
	ids := []string{}
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	for _, id := range ids {
		im := byID[id]
		sort.Strings(im.References)
		for _, release := range s.Images {
			if release.ID == id {
				im.Release = true
				im.ConfigID = release.ConfigID
				if im.ConfigID == "" {
					im.ConfigID = id
				}
				break
			}
		}
		if !im.Release {
			path := filepath.Join(dir, strings.TrimPrefix(id, "sha256:")+".tar")
			if err := savePortableImage(path, id); err != nil {
				return err
			}
			configID, err := archiveConfigID(path)
			if err != nil {
				return err
			}
			im.ConfigID = configID
			a, err := fileArtifact(path)
			if err != nil {
				return err
			}
			im.Archive = &a
		}
		m.Images = append(m.Images, *im)
	}
	return writeJSON(filepath.Join(dir, "manifest.json"), m)
}

// Only the Docker archive's actual config bytes establish portable identity.
// Layers may be large; stream them, and bound the JSON metadata in memory.
func archiveConfigID(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	r := tar.NewReader(f)
	configs := map[string]string{}
	var entries []struct{ Config string }
	seen := map[string]bool{}
	for {
		h, e := r.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", e
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if seen[h.Name] {
			return "", errors.New("duplicate image archive member")
		}
		seen[h.Name] = true
		if h.Size > 8<<20 {
			continue
		}
		b, e := io.ReadAll(r)
		if e != nil {
			return "", e
		}
		if h.Name == "manifest.json" {
			if e = json.Unmarshal(b, &entries); e != nil {
				return "", e
			}
		} else {
			var cfg struct {
				Architecture, OS string
				RootFS           struct{ Type string }
			}
			if json.Unmarshal(b, &cfg) == nil && cfg.Architecture == "amd64" && cfg.OS == "linux" && cfg.RootFS.Type == "layers" {
				sum := sha256.Sum256(b)
				configs[h.Name] = "sha256:" + hex.EncodeToString(sum[:])
			}
		}
	}
	if len(entries) != 1 || configs[entries[0].Config] == "" {
		return "", errors.New("image archive must contain one Linux amd64 image config")
	}
	return configs[entries[0].Config], nil
}

func fileArtifact(path string) (artifact, error) {
	f, err := os.Open(path)
	if err != nil {
		return artifact{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return artifact{}, err
	}
	if n > 64<<30 {
		return artifact{}, errors.New("镜像归档超过单文件 64 GiB 限制")
	}
	return artifact{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}

func restoreBackupImages(dir, data string) error {
	_, err := restoreImageInventory(dir, data)
	return err
}

func restoreImageInventory(dir, data string, known ...map[string]string) (map[string]string, error) {
	mapping := map[string]string{}
	if len(known) > 0 {
		for ref, id := range known[0] {
			mapping[ref] = id
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if os.IsNotExist(err) {
		if err = remapSandboxImages(data, mapping); err != nil {
			return nil, fmt.Errorf("旧备份缺少工作空间镜像，请补充原镜像或重新备份: %w", err)
		}
		return mapping, nil
	}
	if err != nil {
		return nil, err
	}
	var m backupImageManifest
	if json.Unmarshal(b, &m) != nil || (m.Schema != 1 && m.Schema != 2) {
		return nil, errors.New("invalid backup image manifest")
	}
	seen := map[string]bool{}
	for _, im := range m.Images {
		if !validImageID(im.ID) || seen[im.ID] || len(im.References) == 0 || (m.Schema == 2 && !validImageID(im.ConfigID)) {
			return nil, errors.New("invalid backup image record")
		}
		seen[im.ID] = true
	}
	// Verify every archive before importing any. Schema 1 remains strict because
	// it contains no trustworthy source-ID to config-ID mapping.
	paths := []string{}
	for _, im := range m.Images {
		if im.Release {
			continue
		}
		name, a := strings.TrimPrefix(im.ID, "sha256:")+".tar", im.Archive
		if m.Schema == 1 {
			name, a = "images.tar", m.Archive
		}
		if a == nil {
			return nil, errors.New("backup image archive missing")
		}
		path, e := contained(dir, name)
		if e != nil {
			return nil, e
		}
		if e = verifyFile(path, *a); e != nil {
			return nil, e
		}
		if m.Schema == 2 {
			config, e := archiveConfigID(path)
			if e != nil || config != im.ConfigID {
				return nil, errors.New("backup image config digest mismatch")
			}
		}
		if len(paths) == 0 || m.Schema == 2 {
			paths = append(paths, path)
		}
	}
	for _, path := range paths {
		if err = execute("docker", "image", "load", "-i", path); err != nil {
			return nil, err
		}
	}
	for _, im := range m.Images {
		id, e := inspectImage(im.ID)
		if e != nil && m.Schema == 2 {
			id, e = inspectImage(im.ConfigID)
		}
		if e == nil && id != im.ID && id != im.ConfigID {
			id, e = verifyPortableImage(im.ConfigID, im.ConfigID)
		}
		if e != nil && mapping[im.ID] != "" {
			id, e = inspectImage(mapping[im.ID])
		}
		if e != nil && m.Schema == 2 && !im.Release {
			id, e = verifyPortableImage(backupImageTag(im.ID), im.ConfigID)
		}
		if e != nil {
			return nil, fmt.Errorf("恢复镜像身份不匹配: %s", im.ID)
		}
		for _, ref := range append([]string{im.ID, im.ConfigID}, im.References...) {
			if ref == "" {
				continue
			}
			if strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\r\n\x00 ") {
				return nil, errors.New("invalid image alias")
			}
			if previous := mapping[ref]; previous != "" && previous != id {
				return nil, errors.New("conflicting backup image reference")
			}
			mapping[ref] = id
			if !strings.HasPrefix(ref, "sha256:") && !strings.Contains(ref, "@") {
				if e = execute("docker", "image", "tag", id, ref); e != nil {
					return nil, e
				}
			}
		}
	}
	if err = remapSandboxImages(data, mapping); err != nil {
		return nil, err
	}
	return mapping, nil
}

func remapSandboxImages(data string, mapping map[string]string) error {
	files, err := filepath.Glob(filepath.Join(data, "runtime/sandboxes/*/*/*/*/vm/runtime.json"))
	if err != nil {
		return err
	}
	for _, file := range files {
		b, e := os.ReadFile(file)
		if e != nil {
			return e
		}
		var vm map[string]json.RawMessage
		if e = json.Unmarshal(b, &vm); e != nil {
			return e
		}
		var ref string
		if e = json.Unmarshal(vm["image"], &ref); e != nil {
			return e
		}
		if mapped := mapping[ref]; mapped != "" && mapped != ref {
			vm["image"], _ = json.Marshal(mapped)
			if e = writeJSON(file, vm); e != nil {
				return e
			}
			ref = mapped
		}
		if _, e = inspectImage(ref); e != nil {
			return e
		}
	}
	return nil
}
