package runtimeadapter

import (
	"context"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

// The retained Sandbox environment reflects its creation intent. Stage the
// latest model as one atomic credential/endpoint pair before each native Run,
// including restarts that only change LLM and have no user ConfigFiles.
// Call after user ConfigFiles so they cannot replace the controlled selection.
func (c *Client) writeNativeModel(ctx context.Context, env Environment, task taskflow.CreateTaskReq) error {
	if task.CodingAgent != taskflow.CodingAgentCodex && task.CodingAgent != taskflow.CodingAgentClaude {
		return nil
	}
	data := mustJSON(map[string]string{
		"task_id":  task.ID.String(),
		"api_key":  task.LLM.ApiKey,
		"base_url": task.LLM.BaseURL,
		"model":    task.LLM.Model,
	})
	chunks := make(chan []byte, 1)
	chunks <- data
	close(chunks)
	mode := uint32(0600)
	return (&fileClient{c}).upload(ctx, taskflow.FileReq{ID: env.ID, Path: "/data/state/jingjiaagent-native/" + task.ID.String() + ".model.json", UserID: env.OwnerID}, chunks, &mode, true)
}
