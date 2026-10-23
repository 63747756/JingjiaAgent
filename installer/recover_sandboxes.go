package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Full-host recovery intentionally recreates the disposable container layer.
// Only run against the extracted backup, before publishing it or starting the
// daemon. Ordinary resume must retain the runtime's missing-container guard.
func prepareRecoveredSandboxes(data string) error {
	root := filepath.Join(data, "runtime/sandboxes")
	files, err := filepath.Glob(filepath.Join(root, "*", "*", "*", "*", "vm", "runtime.json"))
	if err != nil {
		return err
	}
	for _, file := range files {
		dir := filepath.Dir(filepath.Dir(file))
		read := func(name string, dst any) error {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				return err
			}
			return json.Unmarshal(b, dst)
		}
		var vm map[string]json.RawMessage
		if err := read("vm/runtime.json", &vm); err != nil {
			return err
		}
		if string(vm["driver"]) != `"docker"` {
			return fmt.Errorf("恢复仅支持 Docker 工作空间: %s", file)
		}
		var metadata struct {
			Summary struct {
				ID, Driver string
				Workspace  string `json:"workspace_path"`
			}
		}
		if err := read("metadata.json", &metadata); err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, dir)
		prefix := "/data/sandboxes/" + filepath.ToSlash(rel) + "/"
		if metadata.Summary.ID != filepath.Base(dir) || metadata.Summary.Driver != "docker" || metadata.Summary.Workspace != prefix+"workspace" {
			return fmt.Errorf("恢复工作空间身份或路径不匹配: %s", dir)
		}
		var manifest struct {
			Version int
			Driver  string
			Mounts  []struct{ HostPath, GuestPath, Type string }
		}
		if err := read("vm/mount-manifest.json", &manifest); err != nil {
			return err
		}
		if manifest.Version != 1 || manifest.Driver != "docker" {
			return fmt.Errorf("恢复工作空间挂载版本不支持: %s", dir)
		}
		workspace := false
		for _, mount := range manifest.Mounts {
			if mount.Type != "bind" || !strings.HasPrefix(mount.HostPath, prefix) {
				return fmt.Errorf("恢复工作空间包含未备份的外部挂载: %s", dir)
			}
			local, err := contained(dir, strings.TrimPrefix(mount.HostPath, prefix))
			if err != nil {
				return err
			}
			real, err := filepath.EvalSymlinks(local)
			if err != nil || (real != dir && !strings.HasPrefix(real, dir+string(filepath.Separator))) {
				return fmt.Errorf("恢复工作空间挂载缺失或越界: %s", local)
			}
			if mount.GuestPath == "/workspace" && mount.HostPath == prefix+"workspace" {
				workspace = true
			}
		}
		if !workspace {
			return fmt.Errorf("恢复工作空间缺少持久化工作目录: %s", dir)
		}
		// A cold backup stops containers directly; its persisted summary may
		// still say running/pending. Mark the extracted summary stopped too,
		// otherwise daemon startup treats it as runtime loss and reintroduces
		// StoppedAt, blocking intentional reconstruction on the fresh engine.
		// Preserve unknown fields and credentials as opaque JSON values.
		var raw, summary map[string]json.RawMessage
		if err := read("metadata.json", &raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw["summary"], &summary); err != nil {
			return err
		}
		summary["vm_status"] = json.RawMessage(`"STOPPED"`)
		raw["summary"], err = json.Marshal(summary)
		if err != nil {
			return err
		}
		if info, err := os.Stat(filepath.Join(dir, "home/.opencode/xdg-data/opencode")); err == nil && info.IsDir() {
			var env []map[string]json.RawMessage
			if b := raw["env_items"]; b != nil {
				if err := json.Unmarshal(b, &env); err != nil {
					return err
				}
			}
			found := false
			for _, item := range env {
				if string(item["name"]) == `"XDG_DATA_HOME"` {
					if string(item["value"]) != `"/root/.opencode/xdg-data"` {
						return fmt.Errorf("工作空间自定义 XDG_DATA_HOME 需要人工迁移: %s", dir)
					}
					found = true
				}
			}
			if !found {
				env = append(env, map[string]json.RawMessage{"name": json.RawMessage(`"XDG_DATA_HOME"`), "value": json.RawMessage(`"/root/.opencode/xdg-data"`)})
			}
			raw["env_items"], err = json.Marshal(env)
			if err != nil {
				return err
			}
		}
		if err = writeJSON(filepath.Join(dir, "metadata.json"), raw); err != nil {
			return err
		}
		// Keep the original metadata for diagnosis, and preserve all provider,
		// conversation, workspace and home files without changing their contents.
		if err := copyFile(file, file+".before-recovery", 0600); err != nil {
			return err
		}
		for _, key := range []string{"box_id", "stopped_at", "started_at", "start_attempted_at", "last_error"} {
			delete(vm, key)
		}
		if err := writeJSON(file, vm); err != nil {
			return err
		}
	}
	return nil
}
