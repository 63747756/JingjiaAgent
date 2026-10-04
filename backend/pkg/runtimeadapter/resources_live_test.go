package runtimeadapter

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func liveResources(t *testing.T) (*taskflow.AgentResources, []taskflow.ConfigFile, string, string, string, *atomic.Int32) {
	t.Helper()
	skillReceipt := "SKILL_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	ruleReceipt := "RULE_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	pluginReceipt := "PLUGIN_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	archives := map[string][]byte{}
	pack := func(name, entry, content string) {
		t.Helper()
		var buffer bytes.Buffer
		archive := zip.NewWriter(&buffer)
		file, err := archive.Create(entry)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = file.Write([]byte(content))
		if err = archive.Close(); err != nil {
			t.Fatal(err)
		}
		archives[name] = buffer.Bytes()
	}
	pack("skill", "runtime-proof/SKILL.md", "---\nname: runtime-proof\ndescription: Returns the RUNTIME_SKILL_RECEIPT on request.\n---\nWhen this skill is loaded, respond with only "+skillReceipt+". Do not call other tools.\n")
	pack("plugin", "index.ts", `import fs from "node:fs"; export const RuntimeProof = async () => { fs.writeFileSync("/workspace/plugin-loaded.txt", "`+pluginReceipt+`"); return {}; };`)
	token, calls := uuid.NewString(), &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, exists := archives[strings.TrimPrefix(r.URL.Path, "/")]
		if r.Method != http.MethodGet || r.URL.Query().Get("token") != token || !exists {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	u.Host = "host.docker.internal:" + u.Port()
	address := u.String()
	refs := &taskflow.AgentResources{
		Skills:  []*taskflow.AgentResourceAssetRef{{Name: "runtime-proof", Version: "1", ZipURL: address + "/skill?token=" + token}},
		Plugins: []*taskflow.AgentResourceAssetRef{{Name: "runtime-proof-plugin", Version: "1", ZipURL: address + "/plugin?token=" + token, EntryFilename: "index.ts"}},
	}
	configs := []taskflow.ConfigFile{
		{Path: "${HOME}/.codingmatrix/project-tpl/.ai-ready/rules/runtime-proof.md", Content: "When the user requests RUNTIME_RULE_RECEIPT, reply with exactly " + ruleReceipt + ". Do not call tools for that request.\n"},
		{Path: "~/.config/opencode/opencode.json", Content: `{"instructions":["${HOME}/.codingmatrix/project-tpl/.ai-ready/rules/*.md"],"skills":{"paths":["${HOME}/.codingmatrix/project-tpl/.ai-ready/skills/"]}}`},
	}
	return refs, configs, skillReceipt, ruleReceipt, pluginReceipt, calls
}
