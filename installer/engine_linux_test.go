//go:build linux

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func engineFixture(t *testing.T) (string, *manifest, managedEnginePaths, string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	// Expose only the shell helpers our stubs need. Keeping system directories
	// in PATH lets a preinstalled Docker bypass the managed installation tests.
	for _, name := range []string{"rm", "touch"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	log := filepath.Join(root, "systemctl.log")
	t.Setenv("ENGINE_CALL_LOG", log)
	t.Setenv("ENGINE_READY", filepath.Join(root, "ready"))
	t.Setenv("ENGINE_FAIL", filepath.Join(root, "fail"))
	for name, body := range map[string]string{
		"iptables": "#!/bin/sh\nexit 0\n",
		"systemctl": `#!/bin/sh
printf '%s\n' "$*" >> "$ENGINE_CALL_LOG"
if [ -e "$ENGINE_FAIL" ]; then rm "$ENGINE_FAIL"; exit 1; fi
if [ "$1" = enable ]; then touch "$ENGINE_READY"; fi
`} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cli := []byte(`#!/bin/sh
if [ "$1" = compose ]; then echo 'Docker Compose version v2'; exit 0; fi
if [ "$1" = info ] && [ -e "$ENGINE_READY" ]; then
echo '{"OSType":"linux","Architecture":"x86_64","ServerVersion":"28.5.2","MemoryLimit":true,"CPUCfsQuota":true}'
exit 0
fi
exit 1
`)
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"docker/docker", "docker/dockerd"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(cli))}); err != nil {
			t.Fatal(err)
		}
		tw.Write(cli)
	}
	tw.Close()
	gz.Close()
	m := &manifest{Files: map[string]artifact{}}
	for name, data := range map[string][]byte{"engine/docker.tgz": b.Bytes(), "engine/docker-compose": []byte("#!/bin/sh\nexit 0\n")} {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0700)
		os.WriteFile(p, data, 0600)
		a, err := fileArtifact(p)
		if err != nil {
			t.Fatal(err)
		}
		m.Files[name] = a
	}
	return root, m, managedEnginePaths{dir: filepath.Join(root, "managed"), cli: filepath.Join(bin, "docker"), compose: filepath.Join(root, "plugins/docker-compose"), unit: filepath.Join(root, "systemd/jingjiaagent-docker.service")}, log
}

func TestManagedEngineResumesAfterSystemdFailure(t *testing.T) {
	bundle, m, p, log := engineFixture(t)
	os.WriteFile(os.Getenv("ENGINE_FAIL"), []byte("1"), 0600)
	if ensureDockerAt(bundle, m, p) == nil {
		t.Fatal("injected startup failure not observed")
	}
	if _, err := os.Readlink(p.cli); err != nil {
		t.Fatal("CLI not installed before failure", err)
	}
	if err := ensureDockerAt(bundle, m, p); err != nil {
		t.Fatal("retry failed", err)
	}
	calls, _ := os.ReadFile(log)
	if strings.Count(string(calls), "daemon-reload") != 2 || !strings.Contains(string(calls), "enable --now") {
		t.Fatal(string(calls))
	}
	if err := ensureDockerAt(bundle, m, p); err != nil {
		t.Fatal("healthy reuse", err)
	}
	again, _ := os.ReadFile(log)
	if !bytes.Equal(calls, again) {
		t.Fatal("healthy engine was restarted")
	}
}

func TestManagedEngineResumesWithPluginAlreadyCopied(t *testing.T) {
	bundle, m, p, _ := engineFixture(t)
	if err := installComposeAt(bundle, m, p.compose); err != nil {
		t.Fatal(err)
	}
	if err := ensureDockerAt(bundle, m, p); err != nil {
		t.Fatal(err)
	}
}

func TestManagedEngineDoesNotOverwriteForeignFiles(t *testing.T) {
	for _, which := range []string{"cli", "plugin", "unit", "marker"} {
		t.Run(which, func(t *testing.T) {
			bundle, m, p, log := engineFixture(t)
			path := map[string]string{"cli": p.cli, "plugin": p.compose, "unit": p.unit, "marker": filepath.Join(p.dir, "release.sha256")}[which]
			value := []byte("#!/bin/sh\nexit 1\n")
			os.MkdirAll(filepath.Dir(path), 0700)
			os.WriteFile(path, value, 0700)
			if ensureDockerAt(bundle, m, p) == nil {
				t.Fatal("foreign installation accepted")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(value, after) {
				t.Fatal("foreign file overwritten")
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatal("foreign service started")
			}
		})
	}
}
