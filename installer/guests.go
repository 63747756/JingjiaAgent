package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

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
		b, e = output("docker", "inspect", id)
		if e != nil {
			return nil, e
		}
		var rows []struct {
			ID     string `json:"Id"`
			Name   string
			Mounts []struct{ Type, Source, Destination string }
		}
		if json.Unmarshal(b, &rows) != nil || len(rows) != 1 {
			return nil, errors.New("invalid guest inspection")
		}
		for _, m := range rows[0].Mounts {
			if m.Type == "bind" && m.Destination == "/workspace" && strings.HasPrefix(m.Source, filepath.ToSlash(filepath.Join(s.Data, "runtime/sandboxes"))+"/") {
				result = append(result, rows[0].ID)
				break
			}
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
	for _, id := range ids {
		if valid[id] {
			if e = execute("docker", "start", id); e != nil {
				return e
			}
		}
	}
	return os.Remove(path)
}
