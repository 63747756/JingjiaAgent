package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Older guest images keep OpenCode's SQLite session database in their writable
// layer. Copy it while stopped, including WAL/SHM, into the already persistent
// .opencode mount. Restored guests use this location through XDG_DATA_HOME.
func preserveGuestNativeState(s *deployment) error {
	ids, err := ownedGuests(s, false)
	if err != nil {
		return err
	}
	for _, id := range ids {
		b, err := output("docker", "inspect", id)
		if err != nil {
			return err
		}
		var rows []struct {
			State  struct{ Running bool }
			Config struct{ Env []string }
			Mounts []struct{ Type, Source, Destination string }
		}
		if json.Unmarshal(b, &rows) != nil || len(rows) != 1 || rows[0].State.Running {
			return errors.New("备份原生会话前必须停止工作空间")
		}
		durable := false
		for _, env := range rows[0].Config.Env {
			if env == "XDG_DATA_HOME=/root/.opencode/xdg-data" {
				durable = true
			}
		}
		if durable {
			continue
		}
		var home string
		for _, m := range rows[0].Mounts {
			if m.Type == "bind" && m.Destination == "/root/.opencode" {
				home = m.Source
			}
		}
		base := filepath.Join(s.Data, "runtime/sandboxes") + string(filepath.Separator)
		real, err := filepath.EvalSymlinks(home)
		if err != nil || real != home || !strings.HasPrefix(home, base) {
			return errors.New("原生会话持久目录缺失或越界")
		}
		stage, err := os.MkdirTemp(filepath.Join(s.Data, "runtime"), ".native-backup-*")
		if err != nil {
			return err
		}
		cmd := exec.Command("docker", "cp", "-a", id+":/root/.local/share/opencode", stage)
		result, err := cmd.CombinedOutput()
		if err != nil {
			if strings.Contains(string(result), "Could not find the file /root/.local/share/opencode in container") {
				_ = os.Remove(stage) // Empty; this guest has never used OpenCode.
				continue
			}
			return fmt.Errorf("备份工作空间原生会话失败，暂存目录 %s", stage)
		}
		parent := filepath.Join(home, "xdg-data")
		if err = os.MkdirAll(parent, 0700); err != nil {
			return err
		}
		if real, err = filepath.EvalSymlinks(parent); err != nil || real != parent {
			return errors.New("原生会话备份目录不能是符号链接")
		}
		target := filepath.Join(parent, "opencode")
		if _, err = os.Lstat(target); err == nil {
			if err = os.Rename(target, filepath.Join(stage, "previous")); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err = os.Rename(filepath.Join(stage, "opencode"), target); err != nil {
			return err
		}
		// stage is a private directory created above, never a user supplied path.
		if err = os.RemoveAll(stage); err != nil {
			return err
		}
	}
	return nil
}
