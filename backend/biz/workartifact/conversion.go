package workartifact

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

func (s *Service) convertTask(ctx context.Context, tid uuid.UUID) error {
	// A later round may already have produced a new current version while a
	// converter outage is being retried. Finish the historical versions too.
	rows, err := s.db.QueryContext(ctx, `SELECT v.id,v.artifact_id,v.version,v.filename,v.mime_type,v.size,v.sha256,v.storage_key FROM work_artifacts a JOIN work_artifact_versions v ON v.artifact_id=a.id WHERE a.task_id=$1 AND a.deleted_at IS NULL AND v.validation_status='pending' ORDER BY a.id,v.version`, tid)
	if err != nil {
		return err
	}
	items := []ArtifactVersion{}
	for rows.Next() {
		var v ArtifactVersion
		if err = rows.Scan(&v.ID, &v.ArtifactID, &v.Version, &v.Filename, &v.MIMEType, &v.Size, &v.SHA256, &v.StorageKey); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		if err = s.convertVersion(ctx, v); err != nil {
			return err
		}
	}
	var count, pending, bad int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE v.validation_status='pending'),count(*) FILTER(WHERE v.validation_status<>'passed') FROM work_artifacts a JOIN work_artifact_versions v ON v.id=a.current_version_id WHERE a.task_id=$1 AND a.deleted_at IS NULL`, tid).Scan(&count, &pending, &bad); err != nil {
		return err
	}
	status := "delivered"
	if count == 0 || bad > 0 {
		status = "waiting_confirmation"
	}
	if pending > 0 {
		status = "validating"
	}
	_, err = s.db.ExecContext(ctx, `UPDATE work_tasks SET delivery_status=$2,updated_at=now() WHERE task_id=$1`, tid, status)
	return err
}

func (s *Service) convertVersion(ctx context.Context, v ArtifactVersion) error {
	format := strings.TrimPrefix(strings.ToLower(path.Ext(v.Filename)), ".")
	if format != "docx" && format != "xlsx" && format != "pptx" && format != "pdf" {
		status := "needs_review"
		r, err := s.store.GetObject(ctx, v.StorageKey)
		if err != nil {
			return err
		}
		f, digest, size, err := spool(r, s.cfg.Work.FileLimit())
		r.Close()
		if err != nil {
			return err
		}
		defer removeSpool(f)
		if digest != v.SHA256 || size != v.Size {
			return errors.New("成果内容校验失败")
		}
		b, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		if validateSimpleFile(format, b) {
			status = "passed"
		}
		previewStatus, previewKey := "unavailable", ""
		if mt := nativePreviewMIME(v.Filename); mt != "" {
			previewStatus = "failed"
			if status == "passed" && (!strings.HasPrefix(mt, "text/") || len(b) <= nativeTextPreviewLimit) {
				previewStatus, previewKey = "ready", v.StorageKey
			}
		}
		_, err = s.db.ExecContext(ctx, `UPDATE work_artifact_versions SET validation_status=$2,preview_status=$3,preview_key=$4 WHERE id=$1`, v.ID, status, previewStatus, previewKey)
		return err
	}
	if s.cfg.Work.ConverterURL == "" || s.cfg.Work.ConverterTokenFile == "" || v.Size > 32<<20 {
		_, err := s.db.ExecContext(ctx, `UPDATE work_artifact_versions SET validation_status='needs_review',preview_status='unavailable',validation='{"reason":"文档转换服务未配置或文件超过转换限制"}' WHERE id=$1`, v.ID)
		return err
	}
	u, err := url.Parse(s.cfg.Work.ConverterURL)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("文档转换服务地址无效")
	}
	token, err := os.ReadFile(s.cfg.Work.ConverterTokenFile)
	if err != nil {
		return errors.New("文档转换服务凭据不可读取")
	}
	secret := strings.TrimSpace(string(token))
	if secret == "" || strings.ContainsAny(secret, "\r\n") {
		return errors.New("文档转换服务凭据无效")
	}
	r, err := s.store.GetObject(ctx, v.StorageKey)
	if err != nil {
		return err
	}
	defer r.Close()
	u.Path = strings.TrimRight(u.Path, "/") + "/convert"
	u.RawQuery = url.Values{"format": {format}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = v.Size
	client := &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusServiceUnavailable || res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
		return errors.New("文档转换服务暂时不可用")
	}
	if res.StatusCode != http.StatusOK {
		_, e := s.db.ExecContext(ctx, `UPDATE work_artifact_versions SET validation_status='needs_review',preview_status='failed',validation='{"reason":"格式或版式转换未通过"}' WHERE id=$1`, v.ID)
		return e
	}
	f, _, size, err := spool(res.Body, s.cfg.Work.ArchiveLimit())
	if err != nil {
		return err
	}
	defer removeSpool(f)
	z, err := zip.NewReader(f, size)
	if err != nil {
		return err
	}
	var preview, canonical *zip.File
	var validation map[string]any
	seen := map[string]bool{}
	var total uint64
	for _, entry := range z.File {
		if seen[entry.Name] || entry.Name != path.Clean(entry.Name) || strings.ContainsAny(entry.Name, "\\\x00") || entry.Mode()&os.ModeType != 0 && !entry.FileInfo().IsDir() {
			return errors.New("转换结果文件无效")
		}
		seen[entry.Name] = true
		total += entry.UncompressedSize64
		if total > uint64(s.cfg.Work.ArchiveLimit()) || entry.UncompressedSize64 > uint64(s.cfg.Work.FileLimit()) {
			return errors.New("转换结果超过限制")
		}
		switch entry.Name {
		case "preview.pdf":
			preview = entry
		case "canonical.xlsx":
			if format != "xlsx" {
				return errors.New("转换类型不匹配")
			}
			canonical = entry
		case "validation.json":
			if entry.UncompressedSize64 > 1<<20 {
				return errors.New("转换检查记录过大")
			}
			stream, err := entry.Open()
			if err != nil {
				return err
			}
			b, err := io.ReadAll(io.LimitReader(stream, (1<<20)+1))
			stream.Close()
			if err != nil || json.Unmarshal(b, &validation) != nil {
				return errors.New("转换检查记录无效")
			}
		default:
			if !strings.HasPrefix(entry.Name, "pages/") {
				return errors.New("转换结果包含未知文件")
			}
		}
	}
	if preview == nil || validation == nil {
		return errors.New("转换结果不完整")
	}
	stream, err := preview.Open()
	if err != nil {
		return err
	}
	pf, digest, n, err := spool(stream, s.cfg.Work.FileLimit())
	stream.Close()
	if err != nil {
		return err
	}
	defer removeSpool(pf)
	var magic [5]byte
	if _, err = pf.ReadAt(magic[:], 0); err != nil || string(magic[:]) != "%PDF-" {
		return errors.New("转换预览不是PDF")
	}
	previewKey := fmt.Sprintf("work-private/previews/%s/preview.pdf", v.ID)
	if err = s.putVerified(ctx, previewKey, pf, digest, n); err != nil {
		return err
	}
	status := "passed"
	if validation["status"] != "passed" {
		status = "needs_review"
	}
	if canonical != nil && status == "passed" {
		stream, err := canonical.Open()
		if err != nil {
			return err
		}
		cf, dg, n, err := spool(stream, s.cfg.Work.FileLimit())
		stream.Close()
		if err != nil {
			return err
		}
		defer removeSpool(cf)
		if dg != v.SHA256 {
			vid := uuid.New()
			key := fmt.Sprintf("work-private/artifacts/%s/%s/%s", v.ArtifactID, vid, v.Filename)
			if err = s.putVerified(ctx, key, cf, dg, n); err != nil {
				return err
			}
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			// Serialize with new artifact versions and other conversion workers.
			// A retry must not create another derived version or roll current back.
			var current uuid.UUID
			if err = tx.QueryRowContext(ctx, `SELECT current_version_id FROM work_artifacts WHERE id=$1 FOR UPDATE`, v.ArtifactID).Scan(&current); err != nil {
				return err
			}
			var validationStatus string
			if err = tx.QueryRowContext(ctx, `SELECT validation_status FROM work_artifact_versions WHERE id=$1`, v.ID).Scan(&validationStatus); err != nil {
				return err
			}
			if validationStatus != "pending" {
				return nil
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO work_artifact_versions(id,artifact_id,version,filename,mime_type,storage_key,sha256,size,preview_key,validation_status,preview_status,validation,source_version_id) SELECT $1,$2,max(version)+1,$3,$4,$5,$6,$7,$8,'passed','ready',$9,$10 FROM work_artifact_versions WHERE artifact_id=$2`, vid, v.ArtifactID, v.Filename, v.MIMEType, key, dg, n, previewKey, string(jsonBytes(validation)), v.ID); err != nil {
				return err
			}
			if current == v.ID {
				if _, err = tx.ExecContext(ctx, `UPDATE work_artifacts SET current_version_id=$2 WHERE id=$1`, v.ArtifactID, vid); err != nil {
					return err
				}
			}
			if _, err = tx.ExecContext(ctx, `UPDATE work_artifact_versions SET validation_status='needs_review',preview_status='ready',preview_key=$2,validation=$3 WHERE id=$1`, v.ID, previewKey, string(jsonBytes(validation))); err != nil {
				return err
			}
			return tx.Commit()
		}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE work_artifact_versions SET validation_status=$2,preview_status='ready',preview_key=$3,validation=$4 WHERE id=$1`, v.ID, status, previewKey, string(jsonBytes(validation)))
	return err
}

func validateSimpleFile(format string, b []byte) bool {
	if len(b) == 0 {
		return false
	}
	switch format {
	case "txt", "log", "md", "markdown", "patch", "diff", "py", "js", "jsx", "ts", "tsx", "go", "rs", "java", "c", "h", "cpp", "css", "html", "htm", "h5", "sh", "ps1", "sql", "yaml", "yml":
		// This recognizes text, not code correctness. HTML only executes in an
		// opaque, network-restricted preview sandbox.
		return utf8.Valid(b) && !bytes.ContainsRune(b, 0)
	case "json":
		return json.Valid(b)
	case "csv":
		if !utf8.Valid(b) || bytes.ContainsRune(b, 0) {
			return false
		}
		reader := csv.NewReader(bytes.NewReader(b))
		reader.FieldsPerRecord = -1
		count := 0
		for {
			_, err := reader.Read()
			if err == io.EOF {
				return count > 0
			}
			if err != nil {
				return false
			}
			count++
		}
	case "svg":
		return validPreviewSVG(b)
	case "png", "jpg", "jpeg", "gif", "webp", "bmp":
		cfg, kind, err := image.DecodeConfig(bytes.NewReader(b))
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40000000 {
			return false
		}
		expected := format
		if format == "jpg" {
			expected = "jpeg"
		}
		if kind != expected {
			return false
		}
		_, _, err = image.Decode(bytes.NewReader(b))
		return err == nil
	}
	return false
}
