// Package workspacesnapshot implements the full, private workspace archive.
// The same source is copied into the pinned runtime build by patches/apply.py.
// Links are stored as data and created last; they are never followed on export
// or extraction. Runtime home, credentials and process memory are not included.
package workspacesnapshot

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const Schema = "jingjiaagent.workspace.checkpoint.v3"
const MaxBytes int64 = 2 << 30
const MaxFiles = 100000
const MaxManifestBytes = 32 << 20

type File struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	Type     string `json:"type,omitempty"`
	Mode     uint32 `json:"mode,omitempty"`
	Modified int64  `json:"modified,omitempty"`
}
type Manifest struct {
	Schema       string `json:"schema"`
	CheckpointID string `json:"checkpoint_id"`
	SourceDir    string `json:"source_dir"`
	Files        []File `json:"files"`
}

func member(name string) bool {
	return strings.HasPrefix(name, "workspace/") && len(name) > len("workspace/") &&
		path.Clean(name) == name && !strings.ContainsAny(name, "\\\x00\r\n")
}

// Export must run under the sandbox lifecycle lock with all writers stopped.
// No exclusions: Git history, ignored files, empty directories, attachments,
// work products and safe permission bits are all retained.
func Export(ctx context.Context, workspace, id string, out io.Writer) error {
	info, err := os.Lstat(workspace)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(workspace) || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("工作空间根目录无效")
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	m := Manifest{Schema: Schema, CheckpointID: id, SourceDir: "workspace", Files: []File{}}
	var total int64
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if len(m.Files) >= MaxFiles {
			return fmt.Errorf("完整工作空间超过 %d 个条目的保存限制", MaxFiles)
		}
		if !member("workspace/" + name) {
			return fmt.Errorf("工作空间路径无法归档：%q", name)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := File{Path: "workspace/" + name, Mode: uint32(info.Mode().Perm()), Modified: info.ModTime().UnixNano()}
		h := sha256.New()
		switch {
		case info.Mode().IsRegular():
			item.Type = "file"
			f, err := root.Open(name)
			if err != nil {
				return err
			}
			actual, e := f.Stat()
			if e != nil || !os.SameFile(info, actual) {
				f.Close()
				return errors.New("工作空间在保存期间发生变化")
			}
			item.Size, err = io.Copy(h, io.LimitReader(f, MaxBytes-total+1))
			f.Close()
			if err != nil {
				return err
			}
			if item.Size != info.Size() {
				return errors.New("工作空间在保存期间发生变化")
			}
		case info.IsDir():
			item.Type = "directory"
		case info.Mode()&os.ModeSymlink != 0:
			item.Type = "symlink"
			target, err := root.Readlink(name)
			if err != nil {
				return err
			}
			if strings.ContainsRune(target, 0) || len(target) > 65536 {
				return errors.New("工作空间链接目标无效")
			}
			item.Size = int64(len(target))
			_, _ = h.Write([]byte(target))
		default:
			return fmt.Errorf("无法完整保存特殊文件 %q（仅支持普通文件、目录和符号链接）", name)
		}
		total += item.Size
		if total > MaxBytes {
			return errors.New("完整工作空间超过 2 GiB 保存限制；原环境保留")
		}
		item.SHA256 = hex.EncodeToString(h.Sum(nil))
		m.Files = append(m.Files, item)
		return nil
	})
	if err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(b) > MaxManifestBytes {
		return errors.New("工作空间文件清单过大")
	}
	z := zip.NewWriter(&boundedWriter{out, MaxBytes + MaxManifestBytes + int64(MaxFiles)*512})
	write := func(name string) (io.Writer, error) {
		h := &zip.FileHeader{Name: name, Method: zip.Store}
		h.SetMode(0600)
		return z.CreateHeader(h)
	}
	w, err := write("manifest.json")
	if err != nil {
		return err
	}
	if _, err = w.Write(b); err != nil {
		return err
	}
	for _, item := range m.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		w, err = write(item.Path)
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(item.Path, "workspace/")
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if uint32(info.Mode().Perm()) != item.Mode || info.ModTime().UnixNano() != item.Modified {
			return errors.New("工作空间在保存期间发生变化")
		}
		h := sha256.New()
		var n int64
		switch item.Type {
		case "file":
			if !info.Mode().IsRegular() {
				return errors.New("工作空间文件类型发生变化")
			}
			f, e := root.Open(name)
			if e != nil {
				return e
			}
			n, err = io.Copy(io.MultiWriter(w, h), io.LimitReader(f, item.Size+1))
			f.Close()
		case "symlink":
			if info.Mode()&os.ModeSymlink == 0 {
				return errors.New("工作空间链接类型发生变化")
			}
			target, e := root.Readlink(name)
			if e != nil {
				return e
			}
			count, e := io.WriteString(io.MultiWriter(w, h), target)
			n, err = int64(count), e
		case "directory":
			if !info.IsDir() {
				return errors.New("工作空间目录类型发生变化")
			}
		}
		if err != nil {
			return err
		}
		if n != item.Size || hex.EncodeToString(h.Sum(nil)) != item.SHA256 {
			return errors.New("工作空间在保存期间发生变化")
		}
	}
	return z.Close()
}

const MaxArchiveBytes = MaxBytes + MaxManifestBytes + int64(MaxFiles)*512

type boundedWriter struct {
	out       io.Writer
	remaining int64
}

func (w *boundedWriter) Write(b []byte) (int, error) {
	if int64(len(b)) > w.remaining {
		return 0, errors.New("完整工作空间归档超过限制")
	}
	n, e := w.out.Write(b)
	w.remaining -= int64(n)
	return n, e
}

func ReadManifest(z *zip.Reader) (Manifest, error) {
	var m Manifest
	for _, f := range z.File {
		if f.Name == "manifest.json" {
			if f.UncompressedSize64 > MaxManifestBytes {
				return m, errors.New("保存清单过大")
			}
			r, err := f.Open()
			if err != nil {
				return m, err
			}
			defer r.Close()
			d := json.NewDecoder(io.LimitReader(r, MaxManifestBytes+1))
			d.DisallowUnknownFields()
			if err = d.Decode(&m); err != nil {
				return m, err
			}
			var extra any
			if d.Decode(&extra) != io.EOF {
				return m, errors.New("保存清单有多余内容")
			}
			return m, nil
		}
	}
	return m, errors.New("保存清单缺失")
}

// Verify checks the complete member set and bytes before any extraction.
func Verify(ctx context.Context, z *zip.Reader, id string) (Manifest, error) {
	m, err := ReadManifest(z)
	if err != nil {
		return m, err
	}
	if m.Schema != Schema || m.SourceDir != "workspace" || m.CheckpointID != id || len(m.Files) > MaxFiles || len(m.Files)+1 != len(z.File) {
		return m, errors.New("完整工作空间清单不匹配")
	}
	entries := map[string]*zip.File{}
	for _, f := range z.File {
		if entries[f.Name] != nil || !f.Mode().IsRegular() || (f.Name != "manifest.json" && !member(f.Name)) {
			return m, errors.New("完整工作空间含重复或不安全路径")
		}
		entries[f.Name] = f
	}
	seen := map[string]File{}
	var total int64
	for _, item := range m.Files {
		if err := ctx.Err(); err != nil {
			return m, err
		}
		f := entries[item.Path]
		if _, ok := seen[item.Path]; ok {
			return m, errors.New("完整工作空间清单路径重复")
		}
		if !member(item.Path) || f == nil || item.Size < 0 || item.Size > MaxBytes || f.UncompressedSize64 != uint64(item.Size) || item.Mode > 0777 {
			return m, errors.New("完整工作空间文件信息无效")
		}
		if item.Type != "file" && item.Type != "directory" && item.Type != "symlink" {
			return m, errors.New("完整工作空间文件类型无效")
		}
		if item.Type == "directory" && item.Size != 0 || item.Type == "symlink" && item.Size > 65536 {
			return m, errors.New("完整工作空间目录或链接无效")
		}
		total += item.Size
		if total > MaxBytes {
			return m, errors.New("完整工作空间展开大小超过限制")
		}
		r, err := f.Open()
		if err != nil {
			return m, err
		}
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(r, item.Size+1))
		r.Close()
		if err != nil || n != item.Size || hex.EncodeToString(h.Sum(nil)) != item.SHA256 {
			return m, errors.New("完整工作空间文件校验失败")
		}
		seen[item.Path] = item
	}
	for name := range seen {
		for parent := path.Dir(name); parent != "workspace"; parent = path.Dir(parent) {
			if p, ok := seen[parent]; !ok || p.Type != "directory" {
				return m, errors.New("完整工作空间父目录缺失或经过链接")
			}
		}
	}
	return m, nil
}

// Restore requires an empty destination. Preparation owns and discards a failed
// staging directory; no existing user workspace is ever overwritten here.
func Restore(ctx context.Context, z *zip.Reader, id, destination string) error {
	m, err := Verify(ctx, z, id)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer r.Close()
	entries, err := fs.ReadDir(r.FS(), ".")
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("完整工作空间恢复目标必须为空")
	}
	index := map[string]*zip.File{}
	for _, f := range z.File {
		index[f.Name] = f
	}
	// Manifest order is untrusted. mkdir parents explicitly, but only after
	// verification has proven they are directories, never symlinks.
	var mkdir func(string) error
	mkdir = func(name string) error {
		if name == "." {
			return nil
		}
		if err := mkdir(path.Dir(name)); err != nil {
			return err
		}
		err := r.Mkdir(name, 0700)
		if errors.Is(err, fs.ErrExist) {
			return nil
		}
		return err
	}
	for _, item := range m.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := strings.TrimPrefix(item.Path, "workspace/")
		if item.Type == "directory" {
			if err := mkdir(name); err != nil {
				return err
			}
			continue
		}
		if err := mkdir(path.Dir(name)); err != nil {
			return err
		}
		if item.Type == "symlink" {
			continue
		}
		f, err := r.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		in, e := index[item.Path].Open()
		if e != nil {
			f.Close()
			return e
		}
		_, err = io.Copy(f, in)
		in.Close()
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	for _, item := range m.Files {
		if item.Type == "symlink" {
			in, err := index[item.Path].Open()
			if err != nil {
				return err
			}
			b, err := io.ReadAll(io.LimitReader(in, 65537))
			in.Close()
			if err != nil {
				return err
			}
			if strings.ContainsRune(string(b), 0) || len(b) == 0 {
				return errors.New("完整工作空间链接目标无效")
			}
			if err := r.Symlink(string(b), strings.TrimPrefix(item.Path, "workspace/")); err != nil {
				return err
			}
		}
	}
	// Children before parents, including read-only directories.
	sort.SliceStable(m.Files, func(i, j int) bool { return strings.Count(m.Files[i].Path, "/") < strings.Count(m.Files[j].Path, "/") })
	for i := len(m.Files) - 1; i >= 0; i-- {
		item := m.Files[i]
		if item.Type == "symlink" {
			continue
		}
		name := strings.TrimPrefix(item.Path, "workspace/")
		if err := r.Chmod(name, fs.FileMode(item.Mode)); err != nil {
			return err
		}
		stamp := time.Unix(0, item.Modified)
		if err := r.Chtimes(name, stamp, stamp); err != nil {
			return err
		}
	}
	return nil
}
