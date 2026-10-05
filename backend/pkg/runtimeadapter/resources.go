package runtimeadapter

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

//go:embed guest/resources.py
var resourceScript []byte

func (e *Engine) resources(ctx context.Context, sandbox string, resources *taskflow.AgentResources) error {
	if resources == nil {
		return nil // Preserve the installed selection on a model-only restart.
	}
	data := mustJSON(map[string]any{"resources": resources})
	command := "python3 -c \"import base64;exec(base64.b64decode('" + base64.StdEncoding.EncodeToString(resourceScript) + "'))\" '" + base64.StdEncoding.EncodeToString(data) + "'"
	if len(command) > 120000 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("Guest resource selection exceeds request limit"))
	}
	text, err := e.execute(ctx, sandbox, command, 4096)
	if err != nil {
		return err
	}
	var result struct {
		Error string `json:"error"`
		Code  string `json:"code"`
		Data  struct {
			Installed bool `json:"installed"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(text), &result) != nil {
		return errors.New("invalid Guest resource installation response")
	}
	if result.Error != "" || !result.Data.Installed {
		if result.Code == "invalid_argument" {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("Guest resource package rejected"))
		}
		return errors.New("Guest resource installation failed")
	}
	return nil
}

// ConfigFile paths are produced on either Windows or Linux but consumed by a
// Linux Guest. Expand only the documented HOME placeholder, and only declared
// OpenCode path fields; rule text and credential strings remain literal.
func guestConfigPath(value, home string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	value = strings.ReplaceAll(value, "${HOME}", home)
	return value
}

func renderOpenCodePaths(content, home string, resources *taskflow.AgentResources) (string, error) {
	var cfg map[string]any
	if json.Unmarshal([]byte(content), &cfg) != nil || cfg == nil {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("invalid OpenCode configuration"))
	}
	expand := func(values any) []any {
		items, _ := values.([]any)
		for index, item := range items {
			if value, ok := item.(string); ok {
				items[index] = guestConfigPath(value, home)
			}
		}
		return items
	}
	for _, key := range []string{"instructions", "plugin"} {
		if _, ok := cfg[key]; ok {
			cfg[key] = expand(cfg[key])
		}
	}
	if skills, ok := cfg["skills"].(map[string]any); ok {
		if _, exists := skills["paths"]; exists {
			skills["paths"] = expand(skills["paths"])
		}
	}
	if resources != nil {
		plugins := []string{}
		for _, resource := range resources.Plugins {
			if resource == nil {
				return "", connect.NewError(connect.CodeInvalidArgument, errors.New("invalid plugin resource reference"))
			}
			if resource.EntryFilename == "" {
				continue // Preserve baseline delivery without plugin registration.
			}
			plugins = append(plugins, "file://"+home+"/.codingmatrix/project-tpl/.ai-ready/plugins/"+resource.Name+"/"+resource.EntryFilename)
		}
		cfg["plugin"] = plugins
	}
	data, err := json.Marshal(cfg)
	return string(data), err
}
