package workspacesnapshot

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime/multipart"
	"net/textproto"
	"os"
	"path"
	"strings"
)

const ArtifactsSchema = "jingjiaagent.artifacts.v1"
const ArtifactsMaxBytes int64 = 256 << 20
const ArtifactMaxBytes int64 = 100 << 20
const ArtifactsMaxFiles = 2000
const ArtifactsManifestLimit int64 = 1 << 20

type ArtifactsManifest struct {
	Schema string `json:"schema"`
	ID     string `json:"id"`
	Files  []File `json:"files"`
}

func ArtifactReader(input io.Reader) (*multipart.Reader, error) {
	r := bufio.NewReaderSize(input, 256)
	line, err := r.ReadSlice('\n')
	if err != nil {
		return nil, errors.New("成果传输协议不完整")
	}
	boundary := strings.TrimSuffix(strings.TrimPrefix(string(line), ArtifactsSchema+" "), "\n")
	if !strings.HasPrefix(string(line), ArtifactsSchema+" ") || len(boundary) < 16 || len(boundary) > 70 || strings.ContainsAny(boundary, "\r\n") {
		return nil, errors.New("成果传输协议无效")
	}
	return multipart.NewReader(r, boundary), nil
}

func ArtifactPath(name string) bool {
	return strings.HasPrefix(name, "output/") && len(name) > len("output/") && path.Clean(name) == name && !strings.ContainsAny(name, "\\\x00\r\n")
}

// ExportArtifacts is a bounded transport of output files, not a checkpoint.
// The caller quiesces writers and discards the transport after delivery.
// Work-in-progress, attachments, and repository files are never traversed.
func ExportArtifacts(ctx context.Context, workspace, id string, out io.Writer) error {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	m := ArtifactsManifest{Schema: ArtifactsSchema, ID: id, Files: []File{}}
	missing := false
	for _, dir := range []string{".jingjiaagent", ".jingjiaagent/output"} {
		info, e := root.Lstat(dir)
		if errors.Is(e, fs.ErrNotExist) {
			missing = true
			break
		}
		if e != nil {
			return e
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("成果目录不是普通目录")
		}
	}
	var total int64
	if !missing {
		err = fs.WalkDir(root.FS(), ".jingjiaagent/output", func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			logical := strings.TrimPrefix(name, ".jingjiaagent/")
			if !ArtifactPath(logical) || len(m.Files) >= ArtifactsMaxFiles {
				return errors.New("成果路径无效或文件数量超过保存限制")
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("成果目录包含链接或特殊文件")
			}
			if info.Size() > ArtifactMaxBytes {
				return errors.New("成果单文件超过保存大小限制")
			}
			file, err := root.Open(name)
			if err != nil {
				return err
			}
			h := sha256.New()
			n, err := io.Copy(h, io.LimitReader(file, ArtifactMaxBytes+1))
			file.Close()
			if err != nil {
				return err
			}
			if n != info.Size() {
				return errors.New("成果文件在保存期间发生变化")
			}
			total += n
			if total > ArtifactsMaxBytes {
				return errors.New("成果总大小超过保存限制")
			}
			m.Files = append(m.Files, File{Path: logical, Type: "file", Size: n, SHA256: hex.EncodeToString(h.Sum(nil))})
			return nil
		})
		if err != nil {
			return err
		}
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if int64(len(data)) > ArtifactsManifestLimit {
		return errors.New("成果清单过大")
	}
	w := multipart.NewWriter(&boundedWriter{out, ArtifactsMaxBytes + ArtifactsManifestLimit + int64(ArtifactsMaxFiles)*1024})
	if _, err = io.WriteString(out, ArtifactsSchema+" "+w.Boundary()+"\n"); err != nil {
		return err
	}
	part, err := w.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/json"}, "Content-Id": {"manifest"}})
	if err != nil {
		return err
	}
	if _, err = part.Write(data); err != nil {
		return err
	}
	for _, item := range m.Files {
		if err = ctx.Err(); err != nil {
			return err
		}
		part, err = w.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/octet-stream"}, "Content-Id": {item.Path}})
		if err != nil {
			return err
		}
		file, err := root.Open(".jingjiaagent/" + item.Path)
		if err != nil {
			return err
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(part, h), io.LimitReader(file, item.Size+1))
		file.Close()
		if err != nil {
			return err
		}
		if n != item.Size || hex.EncodeToString(h.Sum(nil)) != item.SHA256 {
			return errors.New("成果文件在保存期间发生变化")
		}
	}
	return w.Close()
}
