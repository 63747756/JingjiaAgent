package runtimeadapter

import (
	"context"
	"path"
	"strings"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

// A rule selection is an explicit manifest, not a glob over old files. Model
// changes keep the task's merged Configs; unused files do not become new rules.
func nativeRuleFiles(task taskflow.CreateTaskReq, home string) []string {
	files := []string{}
	for _, file := range task.Configs {
		name := guestConfigPath(file.Path, home)
		if strings.HasPrefix(name, "~/") {
			name = home + name[1:]
		}
		if path.Dir(name) == home+"/.codingmatrix/project-tpl/.ai-ready/rules" && strings.HasSuffix(name, ".md") {
			files = append(files, name)
		}
	}
	return files
}

func (c *Client) writeNativeRules(ctx context.Context, env Environment, task taskflow.CreateTaskReq, home string) error {
	if task.CodingAgent != taskflow.CodingAgentCodex && task.CodingAgent != taskflow.CodingAgentClaude {
		return nil
	}
	data := mustJSON(map[string]any{"task_id": task.ID.String(), "rules": nativeRuleFiles(task, home)})
	chunks := make(chan []byte, 1)
	chunks <- data
	close(chunks)
	mode := uint32(0600)
	return (&fileClient{c}).upload(ctx, taskflow.FileReq{ID: env.ID, Path: "/data/state/jingjiaagent-native/" + task.ID.String() + ".rules.json", UserID: env.OwnerID}, chunks, &mode, true)
}
