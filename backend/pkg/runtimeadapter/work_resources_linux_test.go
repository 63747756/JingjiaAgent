package runtimeadapter

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

func TestPinnedPluginCacheAndRenewedURLWithActualGuest(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for the Linux Guest installer")
	}
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	entry, err := z.Create("index.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = entry.Write([]byte("export default {}")); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	data := archive.Bytes()
	digest := sha256.Sum256(data)
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate the object store's expiry enforcement; the SDK creates the
		// real signed URLs and the actual Guest script downloads/extracts them.
		signed, e := time.Parse("20060102T150405Z", r.URL.Query().Get("X-Amz-Date"))
		ttl, _ := strconv.Atoi(r.URL.Query().Get("X-Amz-Expires"))
		if e != nil || ttl <= 0 || time.Now().After(signed.Add(time.Duration(ttl)*time.Second)) ||
			r.URL.Path != "/private/plugins/plugin/v1.zip" || r.URL.Query().Get("X-Amz-Signature") == "" {
			http.Error(w, "expired", http.StatusForbidden)
			return
		}
		downloads.Add(1)
		_, _ = w.Write(data)
	}))
	defer server.Close()
	c := resourceRefreshClient()
	c.objectStorage.Endpoint, c.objectStorage.AccessEndpoint = server.URL, server.URL
	expired := server.URL + "/private/plugins/plugin/v1.zip?X-Amz-Date=20200101T000000Z&X-Amz-Expires=900&X-Amz-Signature=expired"
	task := taskflow.CreateTaskReq{
		RoundSkills: &taskflow.RoundSkillSnapshot{},
		AgentResources: &taskflow.AgentResources{Plugins: []*taskflow.AgentResourceAssetRef{{
			Name: "plugin", Version: "v1", EntryFilename: "index.js", S3Key: "plugins/plugin/v1.zip",
			SHA256: hex.EncodeToString(digest[:]), ZipURL: expired,
		}}},
	}
	home := t.TempDir()
	runGuest := func() bool {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		input := base64.StdEncoding.EncodeToString(mustJSON(map[string]any{"resources": task.AgentResources}))
		cmd := exec.CommandContext(ctx, python, "-c", string(resourceScript), input)
		cmd.Env = append(os.Environ(), "HOME="+home)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Guest script failed: %v", err)
		}
		var result struct {
			Error string `json:"error"`
			Data  struct {
				Installed bool `json:"installed"`
			} `json:"data"`
		}
		if err = json.Unmarshal(output, &result); err != nil {
			t.Fatal(err)
		}
		return result.Error == "" && result.Data.Installed
	}
	if runGuest() {
		t.Fatal("cold installation accepted an expired plugin URL")
	}
	if err = c.refreshWorkResources(context.Background(), &task); err != nil || !runGuest() {
		t.Fatalf("initial plugin installation failed: %v", err)
	}
	task.AgentResources.Plugins[0].ZipURL = expired
	if !runGuest() || downloads.Load() != 1 {
		t.Fatal("unchanged pinned package did not reuse its verified local cache")
	}
	root := filepath.Join(home, ".codingmatrix", "project-tpl", ".ai-ready")
	plugin := filepath.Join(root, "plugins", "plugin", "index.js")
	if err = os.WriteFile(plugin, []byte("changed by Guest"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !runGuest() || downloads.Load() != 1 {
		t.Fatal("changed plugin was not repaired from its verified package cache")
	}
	installed, err := os.ReadFile(plugin)
	if err != nil || string(installed) != "export default {}" {
		t.Fatalf("changed plugin was trusted: %s %v", installed, err)
	}
	if err = os.Remove(filepath.Join(root, ".runtime-archives", hex.EncodeToString(digest[:])+".zip")); err != nil {
		t.Fatal(err)
	}
	if runGuest() {
		t.Fatal("missing package cache accepted an expired download URL")
	}
	if err = c.refreshWorkResources(context.Background(), &task); err != nil || !runGuest() {
		t.Fatalf("renewal failed to recover the next round: %v", err)
	}
	installed, err = os.ReadFile(plugin)
	if err != nil || string(installed) != "export default {}" || downloads.Load() != 2 {
		t.Fatalf("next round did not renew the missing pinned package: downloads=%d %v", downloads.Load(), err)
	}
}
