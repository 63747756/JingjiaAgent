package workspaces

// Checkpoints deliberately contain work products, not CLI state or credentials.
import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/chaitin/agent-compose/pkg/workspacesnapshot"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const CheckpointSchema = "jingjiaagent.workspace.checkpoint.v2"
const LegacyCheckpointSchema = "jingjiaagent.workspace.checkpoint.v1"
const CheckpointMaxBytes int64 = 256 << 20
const CheckpointMaxFileBytes int64 = 100 << 20
const CheckpointMaxFiles = 2000
const checkpointManifestMaxBytes = 1 << 20

var checkpointIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)
var checkpointHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type CheckpointFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type CheckpointManifest struct {
	Schema       string           `json:"schema"`
	CheckpointID string           `json:"checkpoint_id"`
	SourceDir    string           `json:"source_dir"`
	Files        []CheckpointFile `json:"files"`
}

func ValidCheckpointID(value string) bool { return checkpointIDPattern.MatchString(value) }

func checkpointPath(value string) bool {
	return value != "" && !strings.ContainsAny(value, "\\\x00:\r\n") && path.Clean(value) == value &&
		!strings.HasPrefix(value, "/") && (strings.HasPrefix(value, "work/") || strings.HasPrefix(value, "output/"))
}

// URL fragments carry integrity metadata through the existing WorkspaceSpec
// wire contract. They are removed before fetching and never reach S3 or logs.
func CheckpointURL(raw, explicitID, explicitHash string) (string, string, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", "", err
	}
	id, hash := strings.TrimSpace(explicitID), strings.ToLower(strings.TrimSpace(explicitHash))
	if strings.HasPrefix(parsed.Fragment, "jingjiaagent-checkpoint=") {
		values, err := url.ParseQuery(parsed.Fragment)
		if err != nil {
			return "", "", "", fmt.Errorf("invalid checkpoint metadata")
		}
		if len(values) != 2 || len(values["jingjiaagent-checkpoint"]) != 1 || len(values["expected_sha256"]) != 1 {
			return "", "", "", fmt.Errorf("invalid checkpoint metadata")
		}
		fragmentID, fragmentHash := values.Get("jingjiaagent-checkpoint"), values.Get("expected_sha256")
		if (id != "" && id != fragmentID) || (hash != "" && hash != fragmentHash) {
			return "", "", "", fmt.Errorf("conflicting checkpoint metadata")
		}
		id, hash = fragmentID, fragmentHash
		parsed.Fragment = ""
	}
	if id != "" || hash != "" {
		if !ValidCheckpointID(id) || !checkpointHashPattern.MatchString(hash) {
			return "", "", "", fmt.Errorf("checkpoint ID and expected SHA256 are required")
		}
	}
	return parsed.String(), id, hash, nil
}

// ExportCheckpoint uses os.Root for containment and rejects all links and
// special files. The caller must hold the sandbox lifecycle lock and ensure
// its runtime is stopped or its processes frozen for the entire operation.
func ExportCheckpoint(ctx context.Context, workspace, id string, destination io.Writer) error {
	if !ValidCheckpointID(id) || !filepath.IsAbs(workspace) {
		return fmt.Errorf("invalid checkpoint arguments")
	}
	info, err := os.Lstat(workspace)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("invalid workspace root")
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	// Archive paths remain logical work/output paths so existing artifacts and
	// versions retain their identities. Only their filesystem root changes.
	prefixes := []string{"work", "output"}
	legacy := false
	for _, prefix := range prefixes {
		_, err := root.Lstat(prefix)
		if err == nil {
			legacy = true
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	managed, err := root.Lstat(".jingjiaagent")
	if errors.Is(err, fs.ErrNotExist) {
		if !legacy {
			prefixes = nil
		}
	} else if err != nil {
		return err
	} else {
		if !managed.IsDir() || managed.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("invalid managed workspace root")
		}
		marker, markerErr := root.Lstat(".jingjiaagent/workspace-layout.json")
		if markerErr != nil && !errors.Is(markerErr, fs.ErrNotExist) {
			return markerErr
		}
		if markerErr == nil && (!marker.Mode().IsRegular() || marker.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("invalid workspace layout receipt")
		}
		// Old idle tasks can be saved before their next message migrates the
		// layout. Preserve that snapshot instead of committing an empty one.
		if !legacy || markerErr == nil {
			platform, err := root.OpenRoot(".jingjiaagent")
			if err != nil {
				return err
			}
			defer platform.Close()
			root = platform
		}
	}
	manifest := CheckpointManifest{Schema: CheckpointSchema, CheckpointID: id, SourceDir: "work/output", Files: []CheckpointFile{}}
	var total int64
	for _, prefix := range prefixes {
		prefixInfo, err := root.Lstat(prefix)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !prefixInfo.IsDir() || prefixInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("checkpoint directory is not a plain directory")
		}
		err = fs.WalkDir(root.FS(), prefix, func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("checkpoint contains a symbolic link")
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || !checkpointPath(name) {
				return fmt.Errorf("checkpoint contains an unsupported entry")
			}
			if len(manifest.Files) >= CheckpointMaxFiles || info.Size() > CheckpointMaxFileBytes || total+info.Size() > CheckpointMaxBytes {
				return fmt.Errorf("checkpoint exceeds limits")
			}
			file, err := root.Open(name)
			if err != nil {
				return err
			}
			defer file.Close()
			actual, err := file.Stat()
			if err != nil {
				return err
			}
			if !os.SameFile(info, actual) || !actual.Mode().IsRegular() {
				return fmt.Errorf("checkpoint changed during export")
			}
			hash := sha256.New()
			size, err := io.Copy(hash, io.LimitReader(file, CheckpointMaxFileBytes+1))
			if err != nil {
				return err
			}
			if size != info.Size() {
				return fmt.Errorf("checkpoint changed during export")
			}
			total += size
			manifest.Files = append(manifest.Files, CheckpointFile{Path: name, Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))})
			return nil
		})
		if err != nil {
			return err
		}
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	payload, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if len(payload) > checkpointManifestMaxBytes {
		return fmt.Errorf("manifest exceeds limits")
	}
	bounded := &checkpointBoundedWriter{writer: destination, remaining: CheckpointMaxBytes}
	output := zip.NewWriter(bounded)
	writeEntry := func(name string, mode fs.FileMode) (io.Writer, error) {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		// Keep work scripts executable without retaining shared writes or
		// privileged bits. Non-executable files remain private.
		header.SetMode(0o600 | mode.Perm()&0o111)
		header.Modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
		return output.CreateHeader(header)
	}
	writer, err := writeEntry("manifest.json", 0o600)
	if err != nil {
		return err
	}
	if _, err = writer.Write(payload); err != nil {
		return err
	}
	for _, entry := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := root.Lstat(entry.Path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("checkpoint changed during export")
		}
		file, err := root.Open(entry.Path)
		if err != nil {
			return err
		}
		writer, err := writeEntry(entry.Path, info.Mode())
		if err != nil {
			file.Close()
			return err
		}
		hash := sha256.New()
		size, copyErr := io.Copy(io.MultiWriter(writer, hash), io.LimitReader(file, entry.Size+1))
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if size != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
			return fmt.Errorf("checkpoint changed during export")
		}
	}
	return output.Close()
}

// Checkpoint ZIPs, including older ones, contain logical paths. Materialize
// them under the managed root rather than alongside repository files.
func CheckpointRestoreTarget(workspace string) (string, error) {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return "", err
	}
	defer root.Close()
	info, err := root.Lstat(".jingjiaagent")
	if errors.Is(err, fs.ErrNotExist) {
		if err := root.Mkdir(".jingjiaagent", 0o700); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("invalid checkpoint restore directory")
	}
	return filepath.Join(workspace, ".jingjiaagent"), nil
}

type checkpointBoundedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *checkpointBoundedWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, fmt.Errorf("checkpoint ZIP exceeds limits")
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	return n, err
}

// ValidateCheckpointArchive verifies hash, path policy, exact member set and
// every file's size/hash before workspace extraction or guest start.
func ValidateCheckpointArchive(ctx context.Context, archivePath, id, expected string) error {
	if !ValidCheckpointID(id) || !checkpointHashPattern.MatchString(expected) {
		return fmt.Errorf("invalid checkpoint integrity metadata")
	}
	source, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(source, workspacesnapshot.MaxArchiveBytes+1))
	source.Close()
	if err != nil {
		return err
	}
	if size > workspacesnapshot.MaxArchiveBytes || hex.EncodeToString(hash.Sum(nil)) != expected {
		return fmt.Errorf("checkpoint archive SHA256 mismatch or size limit")
	}
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	if manifest, err := workspacesnapshot.ReadManifest(&reader.Reader); err == nil && manifest.Schema == workspacesnapshot.Schema {
		_, err = workspacesnapshot.Verify(ctx, &reader.Reader, id)
		return err
	}
	if len(reader.File) > CheckpointMaxFiles+1 {
		return fmt.Errorf("checkpoint has too many entries")
	}
	entries := make(map[string]*zip.File)
	var manifestFile *zip.File
	for _, entry := range reader.File {
		if _, exists := entries[entry.Name]; exists {
			return fmt.Errorf("checkpoint duplicate entry")
		}
		if !entry.Mode().IsRegular() || entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("checkpoint unsupported entry")
		}
		if entry.Name != "manifest.json" && !checkpointPath(entry.Name) {
			return fmt.Errorf("checkpoint unsafe entry")
		}
		entries[entry.Name] = entry
		if entry.Name == "manifest.json" {
			manifestFile = entry
		}
	}
	if manifestFile == nil || manifestFile.UncompressedSize64 > checkpointManifestMaxBytes {
		return fmt.Errorf("checkpoint manifest missing or too large")
	}
	manifestReader, err := manifestFile.Open()
	if err != nil {
		return err
	}
	defer manifestReader.Close()
	var manifest CheckpointManifest
	decoder := json.NewDecoder(io.LimitReader(manifestReader, checkpointManifestMaxBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("invalid checkpoint manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("invalid checkpoint manifest trailing data")
	}
	if (manifest.Schema != CheckpointSchema && manifest.Schema != LegacyCheckpointSchema) || manifest.CheckpointID != id || manifest.SourceDir != "work/output" || len(manifest.Files) != len(entries)-1 {
		return fmt.Errorf("checkpoint manifest contract mismatch")
	}
	seen := make(map[string]bool)
	var total int64
	for _, file := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := entries[file.Path]
		if !checkpointPath(file.Path) || seen[file.Path] || entry == nil || file.Size < 0 || file.Size > CheckpointMaxFileBytes || !checkpointHashPattern.MatchString(file.SHA256) || uint64(file.Size) != entry.UncompressedSize64 {
			return fmt.Errorf("checkpoint manifest file mismatch")
		}
		total += file.Size
		if total > CheckpointMaxBytes {
			return fmt.Errorf("checkpoint expanded size exceeds limit")
		}
		seen[file.Path] = true
		content, err := entry.Open()
		if err != nil {
			return err
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, io.LimitReader(content, file.Size+1))
		closeErr := content.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if size != file.Size || hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
			return fmt.Errorf("checkpoint file SHA256 mismatch")
		}
	}
	return nil
}

// Called before the generic ZIP extractor, which intentionally rejects links.
// Full checkpoints use their verified metadata and restore at workspace root.
func RestoreFullCheckpoint(ctx context.Context, archivePath, id, destination string) (bool, error) {
	z, err := zip.OpenReader(archivePath)
	if err != nil {
		return false, err
	}
	defer z.Close()
	m, err := workspacesnapshot.ReadManifest(&z.Reader)
	if err != nil {
		return false, err
	}
	if m.Schema != workspacesnapshot.Schema {
		return false, nil
	}
	return true, workspacesnapshot.Restore(ctx, &z.Reader, id, destination)
}
