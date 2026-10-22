package workartifact

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/image/bmp"
)

func TestNativePreviewValidation(t *testing.T) {
	for _, format := range []string{"png", "jpg", "gif", "bmp"} {
		var buffer bytes.Buffer
		im := image.NewRGBA(image.Rect(0, 0, 2, 2))
		im.Set(0, 0, color.RGBA{R: 255, A: 255})
		switch format {
		case "png":
			png.Encode(&buffer, im)
		case "jpg":
			jpeg.Encode(&buffer, im, nil)
		case "gif":
			gif.Encode(&buffer, im, nil)
		case "bmp":
			bmp.Encode(&buffer, im)
		}
		if !validateSimpleFile(format, buffer.Bytes()) || validateSimpleFile("webp", buffer.Bytes()) {
			t.Fatalf("image type validation failed: %s", format)
		}
	}
	valid := []string{`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10" fill="red"/></svg>`, `<svg xmlns="http://www.w3.org/2000/svg"><defs><linearGradient id="g"/></defs><rect fill="url(#g)"/></svg>`}
	for _, svg := range valid {
		if !validateSimpleFile("svg", []byte(svg)) {
			t.Fatal("valid SVG rejected")
		}
	}
	invalid := []string{`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`, `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`, `<svg xmlns="http://www.w3.org/2000/svg"><image href="https://remote.invalid/a.png"/></svg>`, `<svg xmlns="http://www.w3.org/2000/svg"><style>@import 'remote.css';</style></svg>`, `<svg xmlns="http://www.w3.org/2000/svg"><rect fill="url(https://remote.invalid/)"/></svg>`, `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject/></svg>`, `<?xml-stylesheet href="remote.css"?><svg xmlns="http://www.w3.org/2000/svg"/>`, `<svg xmlns="http://www.w3.org/2000/svg"><set attributeName="href" to="remote.svg"/></svg>`, `<!DOCTYPE svg><svg xmlns="http://www.w3.org/2000/svg"/>`}
	for _, svg := range invalid {
		if validateSimpleFile("svg", []byte(svg)) {
			t.Fatalf("unsafe SVG accepted: %s", svg)
		}
	}
	for _, ext := range []string{"txt", "md", "markdown", "html", "htm", "h5"} {
		if !validateSimpleFile(ext, []byte("中文内容\n")) || validateSimpleFile(ext, []byte{0xff, 0}) || nativePreviewMIME("file."+strings.ToUpper(ext)) == "" {
			t.Fatalf("invalid native text rules: %s", ext)
		}
	}
	if nativePreviewMIME("report.docx") != "" {
		t.Fatal("Office conversion was classified as a native preview")
	}
	html := previewContentPolicy("text/html; charset=utf-8")
	if !strings.Contains(html, "sandbox allow-scripts;") || strings.Contains(html, "allow-same-origin") || !strings.Contains(html, "connect-src 'none'") {
		t.Fatal("HTML sandbox policy missing")
	}
	if strings.Contains(previewContentPolicy("image/svg+xml"), "allow-scripts") {
		t.Fatal("SVG enables scripts")
	}
}

func TestNativePreviewRoundTripAndLegacyBackfill(t *testing.T) {
	s, owner, reader, project, task := testStorage(t)
	ctx := context.Background()
	for _, filename := range []string{"note.txt", "report.md", "page.html", "mobile.h5", "diagram.svg"} {
		content := []byte("# 中文报告\n第二行")
		if strings.HasSuffix(filename, ".html") || strings.HasSuffix(filename, ".h5") {
			content = []byte(`<h1>H5</h1><script>document.body.dataset.ready='yes'</script>`)
		}
		if strings.HasSuffix(filename, ".svg") {
			content = []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect width="10" height="10"/></svg>`)
		}
		f, hash, n, err := spool(bytes.NewReader(content), 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.saveArtifact(ctx, owner, storageJob{TaskID: task}, "output/"+filename, f, hash, n); err != nil {
			t.Fatal(err)
		}
		removeSpool(f)
		if err = s.convertTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		aid := uuid.NewSHA1(task, []byte("output/"+filename))
		a, err := s.Artifact(ctx, owner, aid)
		if err != nil {
			t.Fatal(err)
		}
		v := a.Versions[0]
		if v.PreviewStatus != "ready" || v.PreviewKey != v.StorageKey {
			t.Fatal("native preview not ready")
		}
		if _, err = s.db.ExecContext(ctx, `UPDATE work_artifact_versions SET preview_status='unavailable',preview_key='' WHERE id=$1`, v.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Versions(ctx, reader, aid); err == nil {
			t.Fatal("private preview exposed")
		}
		var state string
		s.db.QueryRowContext(ctx, `SELECT preview_status FROM work_artifact_versions WHERE id=$1`, v.ID).Scan(&state)
		if state != "unavailable" {
			t.Fatal("unauthorized caller triggered storage access")
		}
		stream, name, mime, err := s.Download(ctx, owner, aid, v.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(stream)
		stream.Close()
		if name != filename || mime != nativePreviewMIME(filename) || !bytes.Equal(body, content) {
			t.Fatal("native preview bytes or MIME changed")
		}
		if err = s.Publish(ctx, owner, aid, project, v.ID); err != nil {
			t.Fatal(err)
		}
		stream, _, _, err = s.Download(ctx, reader, aid, v.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		stream.Close()
	}
}

func TestNativePreviewRejectsCorruptionAndBoundsText(t *testing.T) {
	s, owner, _, _, task := testStorage(t)
	ctx := context.Background()
	for _, content := range [][]byte{[]byte("ok"), bytes.Repeat([]byte("a"), nativeTextPreviewLimit+1)} {
		f, hash, n, _ := spool(bytes.NewReader(content), int64(len(content))+1)
		filename := "small.txt"
		if len(content) > nativeTextPreviewLimit {
			filename = "large.txt"
		}
		if err := s.saveArtifact(ctx, owner, storageJob{TaskID: task}, "output/"+filename, f, hash, n); err != nil {
			t.Fatal(err)
		}
		removeSpool(f)
	}
	if err := s.convertTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	large, err := s.Artifact(ctx, owner, uuid.NewSHA1(task, []byte("output/large.txt")))
	if err != nil {
		t.Fatal(err)
	}
	if large.ValidationStatus != "passed" || large.PreviewStatus != "failed" {
		t.Fatal("oversized text cannot use native preview")
	}
	if _, _, _, err = s.Download(ctx, owner, large.ID, large.CurrentVersionID, true); err == nil {
		t.Fatal("oversized preview returned")
	}
	small, _ := s.Artifact(ctx, owner, uuid.NewSHA1(task, []byte("output/small.txt")))
	v := small.Versions[0]
	s.store.(*memoryStore).objects[v.StorageKey] = []byte("corrupt")
	s.db.ExecContext(ctx, `UPDATE work_artifact_versions SET preview_status='unavailable',preview_key='' WHERE id=$1`, v.ID)
	if _, err = s.Versions(ctx, owner, small.ID); err == nil {
		t.Fatal("legacy backfill ignored content hash")
	}
}
