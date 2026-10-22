package work

import (
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestTaskBranchValidation(t *testing.T) {
	pid := uuid.New()
	for _, branch := range []string{"main", "develop", "feature/修复-123", "release/1.2"} {
		r := CreateRequest{Content: "修改代码", ProjectID: &pid, Branch: branch}
		if err := validateRequest(&r); err != nil {
			t.Fatalf("%s: %v", branch, err)
		}
	}
	for _, branch := range []string{"-option", "../main", "feature//x", "branch.lock", ".hidden", "a@{b}", "a b", "a\nb", "a\\b", "a?", "a:", "a/", "a.", strings.Repeat("a", 256)} {
		r := CreateRequest{Content: "修改代码", ProjectID: &pid, Branch: branch}
		if err := validateRequest(&r); err == nil {
			t.Fatalf("accepted %q", branch)
		}
	}
	if err := validateRequest(&CreateRequest{Content: "独立任务", Branch: "main"}); err == nil {
		t.Fatal("branch without repository project")
	}
}
