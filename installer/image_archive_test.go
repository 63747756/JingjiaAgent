package main

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

func writeImageArchive(t *testing.T, path string, config []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := tar.NewWriter(f)
	for name, data := range map[string][]byte{"config.json": config, "manifest.json": []byte(`[{"Config":"config.json","RepoTags":null,"Layers":[]}]`)} {
		if err = w.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveConfigIdentityUsesBytesAndRejectsWrongPlatform(t *testing.T) {
	path := t.TempDir() + "/image.tar"
	valid := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
	writeImageArchive(t, path, valid)
	sum := sha256.Sum256(valid)
	if got, err := archiveConfigID(path); err != nil || got != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatal(got, err)
	}
	writeImageArchive(t, path, []byte(`{"architecture":"arm64","os":"linux","rootfs":{"type":"layers"}}`))
	if _, err := archiveConfigID(path); err == nil {
		t.Fatal("wrong platform accepted")
	}
}
