package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func recoverDeployment(o options, archive string) error {
	if e := requireLinux(); e != nil {
		return e
	}
	if archive == "" {
		return errors.New("--file and matching --bundle are required")
	}
	bundle, m, e := acquire(o)
	if e != nil {
		return e
	}
	for _, path := range []string{o.Root, o.Data} {
		if !filepath.IsAbs(path) || path == "/" {
			return errors.New("dedicated absolute directories required")
		}
		entries, err := os.ReadDir(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if len(entries) > 0 {
			return errors.New("恢复目标必须为空；原目录请先停机并另行保留")
		}
	}
	stage, e := os.MkdirTemp(filepath.Dir(o.Root), ".jingjiaagent-recovery-*")
	if e != nil {
		return e
	}
	// A failed extraction is retained for diagnosis; existing deployment paths are
	// never deleted or overwritten by recovery.
	dataStage, e := os.MkdirTemp(filepath.Dir(o.Data), ".jingjiaagent-data-recovery-*")
	if e != nil {
		return e
	}
	imageStage, e := os.MkdirTemp(filepath.Dir(o.Root), ".jingjiaagent-image-recovery-*")
	if e != nil {
		return e
	}
	defer os.RemoveAll(imageStage) // Disposable image extraction, not restored data.
	f, e := os.Open(archive)
	if e != nil {
		return e
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		return e
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	seen := map[string]bool{}
	type link struct{ path, target string }
	var links []link
	var state *deployment
	for {
		h, e := reader.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if seen[h.Name] || len(seen) > 1_000_000 {
			return errors.New("invalid backup entries")
		}
		seen[h.Name] = true
		prefix, rel, _ := strings.Cut(strings.TrimSuffix(h.Name, "/"), "/")
		root := stage
		if prefix == "data" {
			root = dataStage
		} else if prefix == "images" {
			root = imageStage
		} else if prefix != "installation" {
			return errors.New("unknown backup section")
		}
		if rel == "" {
			continue
		}
		path, e := contained(root, rel)
		if e != nil {
			return e
		}
		if h.Typeflag == tar.TypeDir {
			if e = os.MkdirAll(path, os.FileMode(h.Mode)&0777); e != nil {
				return e
			}
			if e = os.Chown(path, h.Uid, h.Gid); e != nil {
				return e
			}
			continue
		}
		if h.Typeflag == tar.TypeSymlink {
			if prefix != "data" {
				return errors.New("configuration symlinks are forbidden")
			}
			links = append(links, link{path, h.Linkname})
			continue
		}
		if h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > 64<<30 {
			return errors.New("unsupported backup entry")
		}
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return e
		}
		out, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(h.Mode)&0777)
		if e != nil {
			return e
		}
		_, e = io.CopyN(out, reader, h.Size)
		ce := out.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
		if e = os.Chown(path, h.Uid, h.Gid); e != nil {
			return e
		}
		if prefix == "installation" && rel == "deployment.json" {
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			state = &deployment{}
			if e = json.Unmarshal(b, state); e != nil {
				return e
			}
		}
	}
	if state == nil || state.Root != o.Root || state.Data != o.Data || state.Version != m.Version || state.DatabaseGeneration != m.DatabaseGeneration {
		return errors.New("备份目录、版本或数据库代际不匹配；使用原路径和原签名发行包")
	}
	for _, item := range links {
		if e = os.MkdirAll(filepath.Dir(item.path), 0700); e != nil {
			return e
		}
		if e = os.Symlink(item.target, item.path); e != nil {
			return e
		}
	}
	originalRoot := state.Root
	if e = state.normalizeAddresses(); e != nil {
		return e
	}
	state.Root = stage
	if e = prepareRecoveredTLS(state, o); e != nil {
		return e
	}
	if e = checkSecrets(state); e != nil {
		return e
	}
	state.Root = originalRoot
	if e = ensureDocker(bundle, m); e != nil {
		return e
	}
	// Old containers retain bind mounts to the old directory inode, even after a
	// directory rename. Require a clean engine for full-host recovery.
	containers, e := output("docker", "ps", "-aq")
	if e != nil {
		return e
	}
	if strings.TrimSpace(string(containers)) != "" {
		return errors.New("完整恢复需要没有容器的独立 Docker Engine；请保留原环境，在新的宿主机恢复")
	}
	if e = prepareRecoveredPreviewNetwork(state); e != nil {
		return e
	}
	if e = loadImages(bundle, m, o.Offline); e != nil {
		return e
	}
	releaseMapping := map[string]string{}
	for name, original := range state.Images {
		loaded, ok := m.Images[name]
		if !ok {
			return errors.New("backup release image missing")
		}
		if original.ConfigID != "" && original.ConfigID != loaded.ConfigID {
			return errors.New("backup release image content differs")
		}
		if original.ID != loaded.ID && original.ID != loaded.ConfigID && original.ConfigID == "" {
			return errors.New("backup release has no portable image identity")
		}
		for _, ref := range []string{original.ID, original.ConfigID} {
			if ref != "" {
				releaseMapping[ref] = loaded.ID
			}
		}
	}
	imageMapping, e := restoreImageInventory(imageStage, dataStage, releaseMapping)
	if e != nil {
		return e
	}
	if e = writeJSON(filepath.Join(stage, "recovered-images.json"), imageMapping); e != nil {
		return e
	}
	if e = prepareRecoveredSandboxes(dataStage); e != nil {
		return e
	}
	// Publish a resumable recovery state and the repairing installer together
	// with the configuration, before either restored directory becomes live.
	state.Images = m.Images
	state.Phase = "recovering"
	stagedState := *state
	stagedState.Root = stage
	if e = copyRelease(bundle, &stagedState, m); e != nil {
		return e
	}
	if e = initializeRecovery(&stagedState); e != nil {
		return e
	}
	if e = writeJSON(filepath.Join(stage, "deployment.json"), state); e != nil {
		return e
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	if e = copyFile(exe, filepath.Join(stage, "jingjiaagent"), 0755); e != nil {
		return e
	}
	if entries, err := os.ReadDir(o.Root); err == nil && len(entries) == 0 {
		if e = os.Remove(o.Root); e != nil {
			return e
		}
	}
	if entries, err := os.ReadDir(o.Data); err == nil && len(entries) == 0 {
		if e = os.Remove(o.Data); e != nil {
			return e
		}
	}
	if e = os.Rename(stage, o.Root); e != nil {
		return e
	}
	if e = os.Rename(dataStage, o.Data); e != nil {
		return fmt.Errorf("configuration restored, data retained at %s: %w", dataStage, e)
	}
	if e = render(state); e != nil {
		return e
	}
	if e = saveState(state); e != nil {
		return e
	}
	return start(state)
}
