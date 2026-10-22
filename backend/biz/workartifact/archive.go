package workartifact

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"strings"

	"context"
	"github.com/63747756/jingjiaagent/backend/pkg/workspacesnapshot"
	"github.com/google/uuid"
)

type manifestFile = workspacesnapshot.File
type workspaceManifest = workspacesnapshot.Manifest

func jsonBytes(v any) []byte { b, _ := json.Marshal(v); return b }

// verifyArchive checks the exact complete set before any storage commit or
// restore. ZIP metadata is untrusted, including advertised expanded sizes.
func verifyArchive(f *os.File, size int64, id uuid.UUID, maxFile, maxTotal int64, maxFiles int) (workspaceManifest, *zip.Reader, error) {
	var manifest workspaceManifest
	z, err := zip.NewReader(f, size)
	if err != nil {
		return manifest, nil, err
	}
	if m, e := workspacesnapshot.ReadManifest(z); e == nil && m.Schema == workspacesnapshot.Schema {
		m, e = workspacesnapshot.Verify(context.Background(), z, id.String())
		return m, z, e
	}
	entries := map[string]*zip.File{}
	for _, entry := range z.File {
		name := entry.Name
		if strings.HasSuffix(name, "/") {
			name = strings.TrimSuffix(name, "/")
		}
		if name == "" || strings.ContainsAny(name, "\\\x00\r\n") || path.IsAbs(name) || name != path.Clean(name) || strings.HasPrefix(name, "../") || entry.Mode()&os.ModeType != 0 && !entry.FileInfo().IsDir() {
			return manifest, nil, errors.New("检查点包含不安全文件")
		}
		if entry.FileInfo().IsDir() {
			if name != "work" && name != "output" && !strings.HasPrefix(name, "work/") && !strings.HasPrefix(name, "output/") {
				return manifest, nil, errors.New("检查点目录越界")
			}
			continue
		}
		if _, ok := entries[name]; ok {
			return manifest, nil, errors.New("检查点包含重复路径")
		}
		if name != "manifest.json" && !strings.HasPrefix(name, "work/") && !strings.HasPrefix(name, "output/") {
			return manifest, nil, errors.New("检查点文件越界")
		}
		entries[name] = entry
	}
	mf, ok := entries["manifest.json"]
	if !ok || mf.UncompressedSize64 > 1<<20 {
		return manifest, nil, errors.New("检查点清单缺失或过大")
	}
	r, err := mf.Open()
	if err != nil {
		return manifest, nil, err
	}
	b, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	r.Close()
	if err != nil || len(b) > 1<<20 {
		return manifest, nil, errors.New("检查点清单读取失败")
	}
	if json.Unmarshal(b, &manifest) != nil || (manifest.Schema != "jingjiaagent.workspace.checkpoint.v1" && manifest.Schema != "jingjiaagent.workspace.checkpoint.v2") || manifest.CheckpointID != id.String() || manifest.SourceDir != "work/output" {
		return manifest, nil, errors.New("检查点清单身份不匹配")
	}
	if len(manifest.Files) > maxFiles || len(manifest.Files)+1 != len(entries) {
		return manifest, nil, errors.New("检查点文件集合不匹配")
	}
	seen := map[string]bool{}
	var total int64
	for _, item := range manifest.Files {
		entry, ok := entries[item.Path]
		if !ok || item.Path == "manifest.json" || seen[item.Path] || item.Size < 0 || item.Size > maxFile || entry.UncompressedSize64 != uint64(item.Size) || len(item.SHA256) != 64 {
			return manifest, nil, errors.New("检查点文件元数据无效")
		}
		seen[item.Path] = true
		total += item.Size
		if total > maxTotal {
			return manifest, nil, errors.New("检查点展开后超过大小限制")
		}
		for parent := path.Dir(item.Path); parent != "."; parent = path.Dir(parent) {
			if _, exists := entries[parent]; exists {
				return manifest, nil, errors.New("检查点文件与目录冲突")
			}
		}
		r, err := entry.Open()
		if err != nil {
			return manifest, nil, err
		}
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(r, item.Size+1))
		r.Close()
		if err != nil || n != item.Size || hex.EncodeToString(h.Sum(nil)) != item.SHA256 {
			return manifest, nil, errors.New("检查点文件哈希不匹配")
		}
	}
	return manifest, z, nil
}
