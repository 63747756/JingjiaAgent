package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func backupImageTag(id string) string {
	return "jingjiaagent-backup:" + strings.TrimPrefix(id, "sha256:")
}

func savePortableImage(path, id string) error {
	tag := backupImageTag(id)
	existing, err := inspectImage(tag)
	if err == nil && existing != id {
		return errors.New("reserved backup image tag is already used by different content")
	}
	created := err != nil
	if err = execute("docker", "image", "tag", id, tag); err != nil {
		return err
	}
	if created {
		defer execute("docker", "image", "rm", tag)
	} // Only our temporary alias, never an image ID.
	return execute("docker", "image", "save", "-o", path, tag)
}

// OCI index IDs are engine-specific. Re-export the loaded reference and hash
// its config bytes to prove content equality even on classic -> containerd.
func verifyPortableImage(ref, configID string) (string, error) {
	if !validImageID(configID) {
		return "", errors.New("missing portable image identity")
	}
	id, err := inspectImage(ref)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "jingjiaagent-image-verify-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "image.tar")
	if err = execute("docker", "image", "save", "-o", path, ref); err != nil {
		return "", err
	}
	got, err := archiveConfigID(path)
	if err != nil || got != configID {
		return "", errors.New("loaded image config identity mismatch")
	}
	return id, nil
}
