//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedCLIInstalledAfterRestrictedPathInitialization(t *testing.T) {
	// Simulate sudo's restricted search path and a CLI installed only later.
	t.Setenv("PATH", t.TempDir())
	managedBin := t.TempDir()
	if err := includeCommandDirectory(managedBin); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("docker"); err == nil {
		t.Fatal("fixture must start without Docker")
	}
	script := `#!/bin/sh
case "$1" in
  info) printf '%s\n' '{"OSType":"linux","Architecture":"x86_64","ServerVersion":"28.5.2","MemoryLimit":true,"CPUCfsQuota":true}' ;;
  compose) printf '%s\n' 'Docker Compose version v2.40.3' ;;
  *) exit 9 ;;
esac
`
	if err := os.WriteFile(filepath.Join(managedBin, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if err := preflightDocker(); err != nil {
		t.Fatalf("newly installed CLI not discovered: %v", err)
	}
	if err := compose(&deployment{Root: t.TempDir(), Project: "acceptance"}, "ps"); err != nil {
		t.Fatalf("lifecycle command cannot use installed CLI: %v", err)
	}
	if err := includeCommandDirectory(managedBin); err != nil {
		t.Fatal(err)
	}
	if strings.Count(os.Getenv("PATH"), managedBin) != 1 {
		t.Fatal("repeated initialization duplicated the managed path")
	}
}

func TestManagedCommandPathPreservesExistingCLI(t *testing.T) {
	existing, managed := t.TempDir(), t.TempDir()
	for directory, text := range map[string]string{existing: "existing", managed: "managed"} {
		if err := os.WriteFile(filepath.Join(directory, "docker"), []byte("#!/bin/sh\nprintf '%s' "+text+"\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", existing)
	if err := includeCommandDirectory(managed); err != nil {
		t.Fatal(err)
	}
	b, err := output("docker", "info")
	if err != nil || string(b) != "existing" {
		t.Fatalf("existing Docker command must retain precedence: %q %v", b, err)
	}
}
