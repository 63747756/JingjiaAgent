package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func allowedHosts(raw string) ([]string, error) {
	result := []string{}
	seen := map[string]bool{}
	for _, authority := range strings.Split(raw, ",") {
		authority = strings.TrimSpace(authority)
		if authority == "" {
			continue
		}
		u, err := url.Parse("http://" + authority)
		if err != nil {
			return nil, errors.New("invalid allowed host")
		}
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(authority, "*\n\r\t $") {
			return nil, errors.New("allow-hosts requires exact intranet host:port entries")
		}
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() == "localhost" || (ip != nil && !ip.IsPrivate()) {
			return nil, errors.New("allow-hosts only permits intranet destinations")
		}
		if !seen[authority] {
			seen[authority] = true
			result = append(result, authority)
		}
	}
	return result, nil
}

func configureNetwork(s *deployment, raw string) error {
	hosts, e := allowedHosts(raw)
	if e != nil {
		return e
	}
	unlock, e := lock(s.Root)
	if e != nil {
		return e
	}
	defer unlock()
	path := filepath.Join(s.Root, "config/server/config.yaml")
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	var cfg object
	if e = json.Unmarshal(b, &cfg); e != nil {
		return e
	}
	work, ok := cfg["work"].(map[string]any)
	if !ok {
		return errors.New("missing work configuration")
	}
	if e = stop(s); e != nil {
		return e
	}
	s.AllowedHosts = hosts
	work["allowed_model_hosts"] = hosts
	if e = writeJSON(path, cfg); e != nil {
		return e
	}
	if e = saveState(s); e != nil {
		return e
	}
	if e = render(s); e != nil {
		return e
	}
	return startRendered(s)
}
