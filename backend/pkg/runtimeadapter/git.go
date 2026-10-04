package runtimeadapter

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/url"

	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/google/uuid"
)

//go:embed guest/git_credential.py
var gitCredentialScript []byte

func (c *Client) writeGitCredentialBridge(ctx context.Context, env Environment, task taskflow.CreateTaskReq) error {
	if env.Request.Git.URL == "" {
		return nil
	}
	repo, err := url.Parse(env.Request.Git.URL)
	if err != nil {
		return errors.New("invalid Git repository URL")
	}
	if repo.Scheme != "http" && repo.Scheme != "https" {
		return nil // SSH keeps its existing transport and key behavior.
	}
	if task.ID == uuid.Nil || task.LLM.ApiKey == "" || c.gitCredentialURL == "" {
		return errors.New("runtime Git credential bridge is not configured")
	}
	_, engine, err := c.environment(ctx, env.ID)
	if err != nil {
		return err
	}
	root := "/data/state/monkeycode-git/" + task.ID.String()
	config, err := json.Marshal(map[string]string{"task_id": task.ID.String(), "vm_id": env.ID, "endpoint": c.gitCredentialURL,
		"token": task.LLM.ApiKey, "repo": repo.String()})
	if err != nil {
		return err
	}
	for _, file := range []struct {
		path    string
		content []byte
		mode    uint32
	}{{root + ".py", gitCredentialScript, 0700}, {root + ".json", config, 0600}} {
		chunks := make(chan []byte, 1)
		chunks <- file.content
		close(chunks)
		if err := (&fileClient{c}).upload(ctx, taskflow.FileReq{ID: env.ID, Path: file.path, UserID: env.OwnerID}, chunks, &file.mode, true); err != nil {
			return err
		}
	}
	// The empty value resets inherited helpers. On later turns there are
	// already two local values (reset + scoped helper), so replace all of
	// them before adding this task's helper again.
	_, err = engine.execute(ctx, env.SandboxID, "git config --local --replace-all credential.helper '' && git config --local --add credential.helper '!python3 "+root+".py' && git config --local credential.useHttpPath true", 4096)
	return err
}
