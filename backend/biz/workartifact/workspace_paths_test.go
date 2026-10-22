package workartifact

import "testing"

func TestManagedOutputPathsKeepArtifactIdentity(t *testing.T) {
	for _, name := range []string{"output/report.md", "/workspace/output/report.md", ".jingjiaagent/output/report.md", "/workspace/.jingjiaagent/output/report.md"} {
		logical, valid := logicalOutputPath(name)
		if !valid || logical != "output/report.md" {
			t.Fatalf("artifact identity changed: %s => %s", name, logical)
		}
	}
	for _, name := range []string{".jingjiaagent/work/report.md", "/workspace/.jingjiaagent/output/../private", "/tmp/output/report.md", ".jingjiaagent/output//report.md"} {
		if validOutputPath(name) {
			t.Fatalf("accepted invalid output path: %s", name)
		}
	}
}
