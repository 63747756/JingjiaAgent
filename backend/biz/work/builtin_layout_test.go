package work

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"io"
	"strings"
	"testing"
)

func TestBuiltinWorkspaceLayoutUpgradeKeepsAdministratorEdits(t *testing.T) {
	packages, err := skillPackages()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range packages {
		if len(p.PreviousSHA256) != 1 || !builtinLayoutUpgrade(p, sql.NullString{Valid: true, String: p.PreviousSHA256[0]}) {
			t.Fatal("original official version is not eligible for layout upgrade", p.ID)
		}
		for _, active := range []sql.NullString{{}, {Valid: true, String: p.SHA256}, {Valid: true, String: strings.Repeat("a", 64)}} {
			if builtinLayoutUpgrade(p, active) {
				t.Fatal("current or administrator-edited version would be overwritten", p.ID)
			}
		}
		data, err := verifiedReleasePackage(p)
		if err != nil {
			t.Fatal(err)
		}
		archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range archive.File {
			if file.Name != "SKILL.md" {
				continue
			}
			stream, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			content, err := io.ReadAll(stream)
			stream.Close()
			if err != nil || !strings.Contains(string(content), "/workspace/.jingjiaagent/output") {
				t.Fatal("built-in skill uses the old output location", p.ID, err)
			}
			for _, legacy := range []string{"/workspace/input", "/workspace/project", "/workspace/output", "/workspace/work"} {
				if strings.Contains(string(content), legacy) {
					t.Fatal("obsolete directory remains in a built-in skill", p.ID, legacy)
				}
			}
		}
	}
}
