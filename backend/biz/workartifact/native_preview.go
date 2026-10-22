package workartifact

import (
	"bytes"
	"encoding/xml"
	"io"
	"path"
	"strings"
	"unicode/utf8"
)

const nativeTextPreviewLimit = 4 << 20

func nativePreviewMIME(filename string) string {
	switch strings.ToLower(path.Ext(filename)) {
	case ".txt", ".log":
		return "text/plain; charset=utf-8"
	case ".md", ".markdown":
		return "text/markdown; charset=utf-8"
	case ".html", ".htm", ".h5":
		return "text/html; charset=utf-8"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".svg":
		return "image/svg+xml"
	}
	return ""
}

// Policies also apply when a preview URL is opened directly, outside our iframe.
func previewContentPolicy(contentType string) string {
	policy := "default-src 'none'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; media-src data: blob:; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"
	if strings.HasPrefix(contentType, "text/html") {
		return "sandbox allow-scripts; script-src 'unsafe-inline'; " + policy
	}
	return "sandbox; " + policy
}

func validPreviewSVG(b []byte) bool {
	if !utf8.Valid(b) || len(b) > nativeTextPreviewLimit {
		return false
	}
	d := xml.NewDecoder(bytes.NewReader(b))
	depth, roots, tokens := 0, 0, 0
	for {
		token, err := d.Token()
		if err == io.EOF {
			return depth == 0 && roots == 1
		}
		if err != nil || tokens > 100000 {
			return false
		}
		tokens++
		switch item := token.(type) {
		case xml.ProcInst:
			if item.Target != "xml" {
				return false
			}
		case xml.Directive:
			return false
		case xml.StartElement:
			if depth == 0 {
				roots++
				if item.Name.Local != "svg" || item.Name.Space != "http://www.w3.org/2000/svg" || roots != 1 {
					return false
				}
			}
			depth++
			name := strings.ToLower(item.Name.Local)
			if name == "script" || name == "foreignobject" || name == "iframe" || name == "object" || name == "embed" || name == "animate" || name == "set" || name == "animatemotion" || name == "animatetransform" {
				return false
			}
			for _, attr := range item.Attr {
				key, value := strings.ToLower(attr.Name.Local), strings.ToLower(strings.TrimSpace(attr.Value))
				if strings.HasPrefix(key, "on") || ((key == "href" || key == "src") && value != "" && !strings.HasPrefix(value, "#")) || !safeSVGStyle(value) {
					return false
				}
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(item)) != "" || !safeSVGStyle(strings.ToLower(string(item))) {
				return false
			}
		}
	}
}

func safeSVGStyle(value string) bool {
	if strings.Contains(value, "@import") || strings.ContainsAny(value, "\\") {
		return false
	}
	for rest := value; ; {
		i := strings.Index(rest, "url(")
		if i < 0 {
			return true
		}
		rest = rest[i+4:]
		end := strings.Index(rest, ")")
		if end < 0 || !strings.HasPrefix(strings.Trim(strings.TrimSpace(rest[:end]), "\"'"), "#") {
			return false
		}
		rest = rest[end+1:]
	}
}
