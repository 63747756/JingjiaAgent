package usecase

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/63747756/jingjiaagent/backend/templates"
)

// modelConfigFiles renders model credentials/configuration without resolving skills or plugins.
func modelConfigFiles(cli consts.CliName, m *db.Model) (taskflow.CodingAgent, []taskflow.ConfigFile, error) {
	var tmp string
	var path string
	var coding taskflow.CodingAgent
	if m == nil {
		return coding, nil, fmt.Errorf("model is nil")
	}
	npmPackage := "@ai-sdk/openai-compatible"
	thinkingEnabled, contextLimit, outputLimit := modelRuntimeDefaults(m)
	cfs := make([]taskflow.ConfigFile, 0)
	switch cli {
	case consts.CliNameClaude:
		tmp = string(templates.Claude)
		path = "~/.claude/settings.json"
		coding = taskflow.CodingAgentClaude
		m.BaseURL = strings.ReplaceAll(m.BaseURL, "/v1", "")

	case consts.CliNameCodex:
		tmp = string(templates.Codex)
		path = "~/.codex/config.toml"
		coding = taskflow.CodingAgentCodex

	case consts.CliNameOpencode:
		tmp = string(templates.OpenCode)
		path = "~/.config/opencode/opencode.json"
		coding = taskflow.CodingAgentOpenCode

		var err error
		npmPackage, err = opencodeNpmPackage(m.InterfaceType)
		if err != nil {
			return coding, nil, err
		}

		authtemp, err := template.New("auth").Parse(string(templates.OpenCodeAuth))
		if err != nil {
			return coding, nil, err
		}

		var authBuf bytes.Buffer
		if err := authtemp.Execute(&authBuf, map[string]any{
			"api_key": m.APIKey,
		}); err != nil {
			return coding, nil, err
		}
		authMode := uint32(0o600)
		cfs = append(cfs, taskflow.ConfigFile{
			Path:    "~/.local/share/opencode/auth.json",
			Content: authBuf.String(),
			Mode:    &authMode,
		})

	default:
		return coding, nil, fmt.Errorf("unexpected consts.CliName: %#v", cli)
	}

	temp, err := template.New("config").Parse(tmp)
	if err != nil {
		return coding, nil, err
	}

	var buf bytes.Buffer
	if err := temp.Execute(&buf, map[string]any{
		"model":            m.Model,
		"base_url":         m.BaseURL,
		"api_key":          m.APIKey,
		"npm_package":      npmPackage,
		"thinking_enabled": thinkingEnabled,
		"support_image":    m.SupportImage,
		"force_reasoning":  strings.HasPrefix(m.Model, "jingjiaagent-ultra"),
		"context_limit":    contextLimit,
		"output_limit":     outputLimit,
	}); err != nil {
		return coding, nil, err
	}

	cfs = append(cfs, taskflow.ConfigFile{
		Path:    path,
		Content: buf.String(),
	})

	return coding, cfs, nil
}
