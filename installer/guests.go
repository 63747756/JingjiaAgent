package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type guestContainer struct {
	ID     string `json:"Id"`
	Name   string
	Mounts []struct{ Type, Source, Destination string }
	Config struct{ Labels map[string]string }
	State  struct{ Running bool }
}

func inspectGuest(id string) (*guestContainer, error) {
	b, err := output("docker", "inspect", id)
	if err != nil {
		return nil, err
	}
	var rows []guestContainer
	if json.Unmarshal(b, &rows) != nil || len(rows) != 1 || rows[0].ID == "" {
		return nil, errors.New("invalid guest inspection")
	}
	return &rows[0], nil
}

func (g *guestContainer) workspace(s *deployment) string {
	for _, m := range g.Mounts {
		if m.Type == "bind" && m.Destination == "/workspace" && strings.HasPrefix(m.Source, filepath.ToSlash(filepath.Join(s.Data, "runtime/sandboxes"))+"/") {
			return m.Source
		}
	}
	return ""
}

// Select by both the runtime's name and the installation-owned bind path.
// Never stop another installation's task containers for a cold backup.
func ownedGuests(s *deployment, running bool) ([]string, error) {
	args := []string{"ps", "-q", "--filter", "name=agent-compose-"}
	if !running {
		args = append(args, "--all")
	}
	b, e := output("docker", args...)
	if e != nil {
		return nil, e
	}
	ids := strings.Fields(string(b))
	result := []string{}
	for _, id := range ids {
		g, e := inspectGuest(id)
		if e != nil {
			return nil, e
		}
		if g.workspace(s) != "" {
			result = append(result, g.ID)
		}
	}
	return result, nil
}
func suspendGuests(s *deployment) error {
	ids, e := ownedGuests(s, true)
	if e != nil {
		return e
	}
	if len(ids) == 0 {
		return nil
	}
	path := filepath.Join(s.Root, "suspended-guests.json")
	var previous []string
	if b, err := os.ReadFile(path); err == nil {
		if err = json.Unmarshal(b, &previous); err != nil {
			return err
		}
	}
	if e = writeJSON(path, append(previous, ids...)); e != nil {
		return e
	}
	return execute("docker", append([]string{"stop", "--time", "30"}, ids...)...)
}
func resumeGuests(s *deployment) error {
	return resumeGuestsUsing(s, func() (*guestRuntimeClient, error) { return newGuestRuntimeClient(s) })
}

func resumeGuestsUsing(s *deployment, connect func() (*guestRuntimeClient, error)) error {
	path := filepath.Join(s.Root, "suspended-guests.json")
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	var ids []string
	if e = json.Unmarshal(b, &ids); e != nil {
		return e
	}
	owned, e := ownedGuests(s, false)
	if e != nil {
		return e
	}
	valid := map[string]bool{}
	for _, id := range owned {
		valid[id] = true
	}
	var runtime *guestRuntimeClient
	defer func() {
		if runtime != nil {
			runtime.client.CloseIdleConnections()
		}
	}()
	for _, id := range ids {
		if valid[id] {
			g, err := inspectGuest(id)
			if err != nil {
				return err
			}
			sandboxID := g.Config.Labels["agent-compose.sandbox_id"]
			if sandboxID == "" || strings.ContainsAny(sandboxID, "/\\") || g.ID != id ||
				g.Config.Labels["agent-compose.driver"] != "docker" ||
				!strings.HasSuffix(g.workspace(s), "/"+sandboxID+"/workspace") {
				return fmt.Errorf("工作空间 %s 的恢复身份不匹配，已保留恢复清单", id)
			}
			if runtime == nil {
				runtime, err = connect()
				if err != nil {
					return err
				}
			}
			// Docker start alone leaves the daemon's persisted Sandbox STOPPED.
			// Resume only maintenance-owned entries through its lifecycle API;
			// an already-running container is reused, never restarted here.
			if err = runtime.resume(sandboxID); err != nil {
				return fmt.Errorf("工作空间 %s 恢复失败，已保留恢复清单；排查后执行 start 重试: %w", sandboxID, err)
			}
			g, err = inspectGuest(id)
			if err != nil {
				return err
			}
			if g.ID != id || !g.State.Running {
				return fmt.Errorf("工作空间 %s 的原容器未运行，已保留恢复清单", sandboxID)
			}
			delete(valid, id)
		}
	}
	return os.Remove(path)
}
