package runtimeadapter

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/pkg/asseturl"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

//go:embed guest/attachments.py
var attachmentScript []byte

// Browser history uses the original authenticated asset route. Internal
// presigned download origins and their expiring credentials belong only in
// encrypted runtime requests. External user-supplied URLs remain unchanged.
func (c *Client) publicAttachments(owner string, input []taskflow.Attachment) []taskflow.Attachment {
	if !c.objectStorage.Enabled || len(input) == 0 {
		return input
	}
	user, err := uuid.Parse(owner)
	if err != nil {
		return input
	}
	endpoint := strings.TrimSpace(c.objectStorage.AgentAccessEndpoint)
	if endpoint == "" {
		endpoint = strings.TrimSpace(c.objectStorage.AccessEndpoint)
	}
	if endpoint == "" {
		endpoint = strings.TrimSpace(c.objectStorage.Endpoint)
	}
	base, err := url.Parse(endpoint)
	if err != nil || base.Host == "" {
		return input
	}
	bucket := strings.Trim(c.objectStorage.Bucket, "/")
	if bucket == "" {
		return input
	}
	prefix := strings.TrimRight(base.Path, "/")
	if path.Base(prefix) != bucket && (c.objectStorage.ForcePathStyle || !strings.HasPrefix(strings.ToLower(base.Hostname()), strings.ToLower(bucket)+".")) {
		prefix += "/" + bucket
	}
	prefix += "/"
	output := append([]taskflow.Attachment(nil), input...)
	for index, item := range input {
		u, err := url.Parse(item.URL)
		if err != nil || u.Scheme != base.Scheme || !strings.EqualFold(u.Host, base.Host) || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, prefix) {
			continue
		}
		q := u.Query()
		if q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" || q.Get("X-Amz-Signature") == "" || q.Get("X-Amz-Credential") == "" {
			continue
		}
		key, ok := asseturl.CleanKey(strings.TrimPrefix(u.Path, prefix))
		if !ok || !asseturl.AllowedForUser(key, user, c.objectStorage) {
			continue
		}
		output[index].URL = asseturl.Build(key)
	}
	return output
}

func (c *Client) publicInputData(owner string, data []byte) []byte {
	if !c.objectStorage.Enabled {
		return data
	}
	var input map[string]json.RawMessage
	if json.Unmarshal(data, &input) != nil {
		return data
	}
	var attachments []taskflow.Attachment
	if json.Unmarshal(input["attachments"], &attachments) != nil || len(attachments) == 0 {
		return data
	}
	input["attachments"] = mustJSON(c.publicAttachments(owner, attachments))
	return mustJSON(input)
}

func (c *Client) publicInputCallback(owner string, next func(*taskflow.TaskChunk) error) func(*taskflow.TaskChunk) error {
	return func(chunk *taskflow.TaskChunk) error {
		if chunk.Event != "user-input" {
			return next(chunk)
		}
		copy := *chunk
		copy.Data = c.publicInputData(owner, chunk.Data)
		return next(&copy)
	}
}

func (e *Engine) attachments(ctx context.Context, sandbox string, command Command, task taskflow.CreateTaskReq) error {
	_, err := e.stageAttachments(ctx, sandbox, command, task)
	return err
}

func (e *Engine) attachmentLocations(ctx context.Context, sandbox string, command Command, task taskflow.CreateTaskReq) ([]taskflow.AttachmentLocation, error) {
	files, err := e.stageAttachments(ctx, sandbox, command, task)
	if err != nil {
		return nil, err
	}
	if len(files) != len(task.Attachments) {
		return nil, errors.New("Guest attachment receipt is incomplete")
	}
	root := path.Join("/workspace/.jingjiaagent/attachments", task.ID.String(), command.ID)
	for index, file := range files {
		name := task.Attachments[index].Filename
		if name == "" || strings.ContainsAny(name, "/\\") || file.Filename != name || file.Path != path.Join(root, fmt.Sprintf("%d-%s", index+1, name)) {
			return nil, errors.New("Guest attachment receipt path does not match staged input")
		}
	}
	return files, nil
}

func (e *Engine) stageAttachments(ctx context.Context, sandbox string, command Command, task taskflow.CreateTaskReq) ([]taskflow.AttachmentLocation, error) {
	if _, err := runtimeProvider(task.CodingAgent); err != nil {
		return nil, ErrParity
	}
	raw := mustJSON(map[string]any{"task_id": task.ID.String(), "command_id": command.ID, "attachments": task.Attachments, "work_mode": task.Env["JINGJIAAGENT_WORK_MODE"] == "1"})
	// Python expects an empty list rather than Go's nil slice.
	if task.Attachments == nil {
		raw = mustJSON(map[string]any{"task_id": task.ID.String(), "command_id": command.ID, "attachments": []taskflow.Attachment{}, "work_mode": task.Env["JINGJIAAGENT_WORK_MODE"] == "1"})
	}
	script := "python3 -c \"import base64;exec(base64.b64decode('" + base64.StdEncoding.EncodeToString(attachmentScript) + "'))\" '" + base64.StdEncoding.EncodeToString(raw) + "'"
	if len(script) > 120000 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("attachment request exceeds limit"))
	}
	output, err := e.execute(ctx, sandbox, script, 32<<10)
	if err != nil {
		return nil, err
	}
	var result struct {
		Error string `json:"error"`
		Code  string `json:"code"`
		Data  struct {
			Installed bool                          `json:"installed"`
			Files     []taskflow.AttachmentLocation `json:"files"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(output), &result) != nil {
		return nil, errors.New("invalid Guest attachment result")
	}
	if result.Code == "invalid_argument" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("Guest attachments rejected"))
	}
	if result.Error != "" || !result.Data.Installed {
		return nil, errors.New("Guest attachment staging requires reconciliation")
	}
	return result.Data.Files, nil
}
