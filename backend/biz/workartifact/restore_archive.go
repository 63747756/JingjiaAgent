package workartifact

import (
	"archive/zip"
	"context"
	"database/sql"
	"errors"
	"github.com/63747756/jingjiaagent/backend/pkg/workspacesnapshot"
	"github.com/google/uuid"
	"io"
	"os"
	"strings"
)

// Recalculated spreadsheets remain separate immutable versions. Restore overlays
// only a canonical version derived from the exact bytes in this checkpoint.
func (s *Service) restoreArchive(ctx context.Context, tid uuid.UUID, m workspaceManifest, z *zip.Reader) (*os.File, string, int64, error) {
	replacement := map[string]*os.File{}
	defer func() {
		for _, f := range replacement {
			removeSpool(f)
		}
	}()
	for i, item := range m.Files {
		logical := item.Path
		if m.Schema == workspacesnapshot.Schema {
			logical = strings.TrimPrefix(logical, "workspace/.jingjiaagent/")
			if !strings.HasPrefix(logical, "output/") || item.Type != "file" {
				continue
			}
		}
		var key, digest string
		var size int64
		err := s.db.QueryRowContext(ctx, `SELECT c.storage_key,c.sha256,c.size FROM work_artifacts a JOIN work_artifact_versions r ON r.artifact_id=a.id JOIN work_artifact_versions c ON c.source_version_id=r.id WHERE a.task_id=$1 AND a.source_path=$2 AND r.sha256=$3 AND c.validation_status='passed' ORDER BY c.version DESC LIMIT 1`, tid, logical, item.SHA256).Scan(&key, &digest, &size)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, "", 0, err
		}
		r, err := s.store.GetObject(ctx, key)
		if err != nil {
			return nil, "", 0, err
		}
		f, actual, n, err := spool(r, s.cfg.Work.FileLimit())
		r.Close()
		if err != nil {
			return nil, "", 0, err
		}
		if actual != digest || n != size {
			removeSpool(f)
			return nil, "", 0, errors.New("正式成果内容校验失败")
		}
		replacement[item.Path] = f
		m.Files[i].SHA256 = digest
		m.Files[i].Size = size
	}
	if len(replacement) == 0 && (m.Schema == "jingjiaagent.workspace.checkpoint.v2" || m.Schema == workspacesnapshot.Schema) {
		return nil, "", 0, nil
	}
	// Repackage legacy snapshots without changing logical paths or artifact
	// IDs. Old nodes reject v2 before extracting to the obsolete root layout.
	limit := s.cfg.Work.ArchiveLimit()
	if m.Schema == workspacesnapshot.Schema {
		limit = workspacesnapshot.MaxArchiveBytes
	} else {
		m.Schema = "jingjiaagent.workspace.checkpoint.v2"
	}
	out, err := os.CreateTemp("", "work-restore-*.zip")
	if err != nil {
		return nil, "", 0, err
	}
	good := false
	defer func() {
		if !good {
			removeSpool(out)
		}
	}()
	writer := zip.NewWriter(out)
	index := map[string]*zip.File{}
	for _, f := range z.File {
		index[f.Name] = f
	}
	manifest, err := writer.Create("manifest.json")
	if err != nil {
		return nil, "", 0, err
	}
	if _, err = manifest.Write(jsonBytes(m)); err != nil {
		return nil, "", 0, err
	}
	for _, item := range m.Files {
		header := &zip.FileHeader{Name: item.Path, Method: zip.Store}
		// Replacing a spreadsheet must not strip executable bits from other
		// checkpoint files, or grant execution to previously ordinary files.
		header.SetMode(0o600 | index[item.Path].Mode().Perm()&0o111)
		w, err := writer.CreateHeader(header)
		if err != nil {
			return nil, "", 0, err
		}
		if f, ok := replacement[item.Path]; ok {
			if _, err = io.Copy(w, f); err != nil {
				return nil, "", 0, err
			}
		} else {
			r, err := index[item.Path].Open()
			if err != nil {
				return nil, "", 0, err
			}
			_, err = io.Copy(w, r)
			r.Close()
			if err != nil {
				return nil, "", 0, err
			}
		}
	}
	if err = writer.Close(); err != nil {
		return nil, "", 0, err
	}
	info, err := out.Stat()
	if err != nil {
		return nil, "", 0, err
	}
	if info.Size() > limit {
		return nil, "", 0, errors.New("恢复归档超过限制")
	}
	if _, _, err = verifyArchive(out, info.Size(), uuid.MustParse(m.CheckpointID), s.cfg.Work.FileLimit(), s.cfg.Work.ArchiveLimit(), s.cfg.Work.FileCountLimit()); err != nil {
		return nil, "", 0, err
	}
	if _, err = out.Seek(0, io.SeekStart); err != nil {
		return nil, "", 0, err
	}
	// Reuse bounded hashing without retaining the ZIP in memory.
	copy, digest, size, err := spool(out, limit)
	if err != nil {
		return nil, "", 0, err
	}
	removeSpool(out)
	good = true
	return copy, digest, size, nil
}
