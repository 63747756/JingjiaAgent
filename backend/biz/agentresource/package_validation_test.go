package agentresource

import (
	"archive/zip"
	"bytes"
	"os"
	"testing"
)

func TestWorkSkillPackageRejectsAmbiguousOrUnsafeEntries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   []string
		symlink bool
		good    bool
	}{
		{"valid", []string{"SKILL.md", "scripts/check.py"}, false, true},
		{"missing", []string{"readme.md"}, false, false},
		{"traversal", []string{"SKILL.md", "../outside"}, false, false},
		{"duplicate", []string{"SKILL.md", "SKILL.md"}, false, false},
		{"case duplicate", []string{"SKILL.md", "skill.md"}, false, false},
		{"multiple roots", []string{"a/SKILL.md", "b/SKILL.md"}, false, false},
		{"drive", []string{"SKILL.md", "C:/outside"}, false, false},
		{"symlink", []string{"SKILL.md"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			z := zip.NewWriter(&buf)
			for _, name := range tc.files {
				h := &zip.FileHeader{Name: name, Method: zip.Deflate}
				if tc.symlink {
					h.SetMode(os.ModeSymlink | 0755)
				} else {
					h.SetMode(0644)
				}
				w, err := z.CreateHeader(h)
				if err != nil {
					t.Fatal(err)
				}
				w.Write([]byte("fixture"))
			}
			z.Close()
			if err := ValidateSkillPackage(buf.Bytes()); (err == nil) != tc.good {
				t.Fatalf("validation: %v", err)
			}
		})
	}
}
