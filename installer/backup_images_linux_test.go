//go:build linux

package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func imageFixture(t *testing.T) (*deployment, string, string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	os.MkdirAll(bin, 0700)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("IMAGE_LOG", filepath.Join(root, "calls"))
	t.Setenv("IMAGE_LOADED", filepath.Join(root, "loaded"))
	t.Setenv("IMAGE_ALIAS", filepath.Join(root, "alias"))
	t.Setenv("IMAGE_RESTORE", "")
	t.Setenv("OLD_IMAGE", "sha256:"+strings.Repeat("a", 64))
	t.Setenv("NEW_IMAGE", "sha256:"+strings.Repeat("b", 64))
	archive := filepath.Join(root, "fixture.tar")
	config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
	writeImageArchive(t, archive, config)
	sum := sha256.Sum256(config)
	t.Setenv("OLD_CONFIG", "sha256:"+hex.EncodeToString(sum[:]))
	t.Setenv("IMAGE_FIXTURE", archive)
	t.Setenv("IMAGE_PORTABLE", "")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$IMAGE_LOG"
case "$1 $2" in
 'image inspect')
   case "$3" in
     "$NEW_IMAGE") id="$NEW_IMAGE";;
     "$OLD_IMAGE") [ -z "$IMAGE_RESTORE" ] || { [ -z "$IMAGE_PORTABLE" ] && [ -f "$IMAGE_LOADED" ]; } || exit 1; id="$OLD_IMAGE";;
     "$OLD_CONFIG") [ -f "$IMAGE_LOADED" ] || exit 1; id="$OLD_CONFIG";;
     legacy:work) [ -z "$IMAGE_RESTORE" ] || [ -f "$IMAGE_ALIAS" ] || exit 1; id="$OLD_IMAGE";;
     *) exit 1;;
   esac
   printf '[{"Id":"%s","Os":"linux","Architecture":"amd64"}]' "$id";;
 'image save') cp "$IMAGE_FIXTURE" "$4";;
 'image load') touch "$IMAGE_LOADED";;
 'image tag') touch "$IMAGE_ALIAS";;
 'image rm') :;;
 *) exit 1;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return &deployment{Images: map[string]imageRecord{"guest": {ID: os.Getenv("NEW_IMAGE")}}}, filepath.Join(root, "images"), os.Getenv("IMAGE_LOG")
}

func TestBackupRestoresHistoricalImageAndAlias(t *testing.T) {
	s, dir, log := imageFixture(t)
	if err := saveBackupImages(s, dir, []string{"remote:only", "legacy:work"}, []string{os.Getenv("OLD_IMAGE"), os.Getenv("NEW_IMAGE"), os.Getenv("OLD_IMAGE")}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	var m backupImageManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Images) != 2 || m.Schema != 2 || m.Images[0].Archive == nil || m.Images[0].ConfigID != os.Getenv("OLD_CONFIG") {
		t.Fatal("incomplete image inventory")
	}
	calls, _ := os.ReadFile(log)
	for _, line := range strings.Split(string(calls), "\n") {
		if strings.HasPrefix(line, "image save") && (strings.Contains(line, os.Getenv("NEW_IMAGE")) || !strings.HasSuffix(line, backupImageTag(os.Getenv("OLD_IMAGE")))) {
			t.Fatal("incorrect export", line)
		}
	}
	t.Setenv("IMAGE_RESTORE", "1")
	if _, err := inspectImage(os.Getenv("OLD_IMAGE")); err == nil {
		t.Fatal("fixture did not start empty")
	}
	if err := restoreBackupImages(dir, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if id, err := inspectImage("legacy:work"); err != nil || id != os.Getenv("OLD_IMAGE") {
		t.Fatal("alias missing", err)
	}
	calls, _ = os.ReadFile(log)
	if strings.Contains(string(calls), "pull") {
		t.Fatal("recovery used network")
	}
}

func TestBackupRejectsMissingRequiredImage(t *testing.T) {
	s, dir, _ := imageFixture(t)
	if err := saveBackupImages(s, dir, nil, []string{"missing:required"}); err == nil {
		t.Fatal("missing required image accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("incomplete inventory published")
	}
}

func TestRestoreRejectsCorruptImageArchiveBeforeLoad(t *testing.T) {
	s, dir, log := imageFixture(t)
	if err := saveBackupImages(s, dir, nil, []string{os.Getenv("OLD_IMAGE")}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, strings.TrimPrefix(os.Getenv("OLD_IMAGE"), "sha256:")+".tar"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restoreBackupImages(dir, t.TempDir()); err == nil {
		t.Fatal("corrupt image archive accepted")
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "image load") {
		t.Fatal("corrupt archive was loaded")
	}
}

func TestLegacyBackupRejectsMissingSandboxImage(t *testing.T) {
	_, dir, _ := imageFixture(t)
	data := t.TempDir()
	path := filepath.Join(data, "runtime/sandboxes/2026/10/22/task/vm/runtime.json")
	if err := writeJSON(path, map[string]string{"driver": "docker", "image": "missing:legacy"}); err != nil {
		t.Fatal(err)
	}
	if err := restoreBackupImages(dir, data); err == nil {
		t.Fatal("legacy backup silently lost its image")
	}
}

func TestPortableBackupMapsDigestAndSandboxToVerifiedConfig(t *testing.T) {
	s, dir, _ := imageFixture(t)
	if err := saveBackupImages(s, dir, nil, []string{os.Getenv("OLD_IMAGE")}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	var m backupImageManifest
	json.Unmarshal(b, &m)
	digest := "registry/test@" + os.Getenv("OLD_IMAGE")
	m.Images[0].References = append(m.Images[0].References, digest)
	writeJSON(filepath.Join(dir, "manifest.json"), m)
	data := t.TempDir()
	vm := filepath.Join(data, "runtime/sandboxes/2026/10/23/task/vm/runtime.json")
	writeJSON(vm, map[string]string{"driver": "docker", "image": digest, "unknown": "preserved"})
	t.Setenv("IMAGE_RESTORE", "1")
	t.Setenv("IMAGE_PORTABLE", "1")
	mapping, err := restoreImageInventory(dir, data)
	if err != nil || mapping[digest] != os.Getenv("OLD_CONFIG") || mapping[os.Getenv("OLD_IMAGE")] != os.Getenv("OLD_CONFIG") {
		t.Fatal(mapping, err)
	}
	b, _ = os.ReadFile(vm)
	var got map[string]string
	json.Unmarshal(b, &got)
	if got["image"] != os.Getenv("OLD_CONFIG") || got["unknown"] != "preserved" {
		t.Fatal("sandbox not remapped", got)
	}
}

func TestPortableBackupRejectsForgedConfigBeforeImport(t *testing.T) {
	s, dir, log := imageFixture(t)
	if err := saveBackupImages(s, dir, nil, []string{os.Getenv("OLD_IMAGE")}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	var m backupImageManifest
	json.Unmarshal(b, &m)
	m.Images[0].ConfigID = os.Getenv("NEW_IMAGE")
	writeJSON(filepath.Join(dir, "manifest.json"), m)
	if _, err := restoreImageInventory(dir, t.TempDir()); err == nil {
		t.Fatal("forged config accepted")
	}
	b, _ = os.ReadFile(log)
	if strings.Contains(string(b), "image load") {
		t.Fatal("invalid identity imported")
	}
}

func TestRecoveredNodeMapsSuccessiveRestoresAndPreservesSecrets(t *testing.T) {
	block, _ := aes.NewCipher([]byte(strings.Repeat("k", 32)))
	aead, _ := cipher.NewGCM(block)
	plain := []byte(`{"Config":{"guest_image":"original","recovered_images":{"sha256:first":"sha256:second"}},"Material":{"token":"keep"},"future":{"x":1}}`)
	nonce := make([]byte, aead.NonceSize())
	raw := aead.Seal(nonce, nonce, plain, []byte("runtime-node:local"))
	result, err := remapNodeImages(aead, "local", raw, map[string]string{"sha256:second": "sha256:third", "registry/image@sha256:first": "sha256:third", "mutable:tag": "sha256:third"})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := aead.Open(nil, result[:aead.NonceSize()], result[aead.NonceSize():], []byte("runtime-node:local"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Config struct {
			Guest     string            `json:"guest_image"`
			Recovered map[string]string `json:"recovered_images"`
		}
		Material map[string]string
		Future   map[string]int
	}
	json.Unmarshal(decoded, &v)
	if v.Config.Guest != "original" || v.Config.Recovered["sha256:first"] != "sha256:third" || v.Config.Recovered["mutable:tag"] != "" || v.Material["token"] != "keep" || v.Future["x"] != 1 {
		t.Fatal("node contents changed incorrectly")
	}
	if _, err = remapNodeImages(aead, "other", raw, nil); err == nil {
		t.Fatal("wrong node credential accepted")
	}
}

func TestCatalogFailureStillStopsPostgres(t *testing.T) {
	log := fakeLifecycleDocker(t, "", "error")
	s := &deployment{Root: t.TempDir(), Data: t.TempDir(), Project: "test"}
	if err := privateWrite(filepath.Join(s.Data, "postgres/PG_VERSION"), []byte("16")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := backupCatalogReferences(s); err == nil {
		t.Fatal("query failure ignored")
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "--no-deps") || !strings.HasSuffix(string(calls), "stop postgres\n") {
		t.Fatal("database left running", string(calls))
	}
}

func TestCatalogKeepsLocalNodesInstalledImage(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	nonce := make([]byte, aead.NonceSize())
	plain := []byte(`{"Config":{"guest_image":"registry/image@sha256:logical","installed_guest_image":"sha256:installed"}}`)
	payload := aead.Seal(nonce, nonce, plain, []byte("runtime-node:local"))
	query, _ := json.Marshal(map[string]any{"images": []string{"personal:latest"}, "nodes": []map[string]string{{"id": "local", "payload": hex.EncodeToString(payload)}}})
	log := fakeLifecycleDocker(t, "", string(query))
	s := &deployment{Root: t.TempDir(), Data: t.TempDir(), Project: "test"}
	privateWrite(filepath.Join(s.Data, "postgres/PG_VERSION"), []byte("16"))
	privateWrite(filepath.Join(s.Root, "secrets/payload.key"), key)
	optional, required, err := backupCatalogReferences(s)
	if err != nil || len(optional) != 1 || len(required) != 1 || required[0] != "sha256:installed" {
		t.Fatal(optional, required, err)
	}
	calls, _ := os.ReadFile(log)
	if !strings.HasSuffix(string(calls), "stop postgres\n") {
		t.Fatal("catalog database left running")
	}
	t.Setenv("FIXTURE_QUERY", strings.Replace(string(query), hex.EncodeToString(payload), strings.Repeat("00", len(payload)), 1))
	if _, _, err = backupCatalogReferences(s); err == nil {
		t.Fatal("corrupt encrypted configuration ignored")
	}
}
