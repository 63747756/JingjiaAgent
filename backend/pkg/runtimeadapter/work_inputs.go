package runtimeadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/oss"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

type workInputPin struct {
	VersionID uuid.UUID `json:"version_id"`
	Filename  string    `json:"filename"`
	SHA256    string    `json:"sha256"`
}

func (c *Client) stageWorkInputs(ctx context.Context, e Environment, cmd Command, task *taskflow.CreateTaskReq) error {
	if !c.ledger.workEnabled {
		return nil
	}
	// Assistant instructions, project rules and output conventions already
	// belong to SystemPrompt. Staging must never rewrite the user's message.
	var raw []byte
	err := c.ledger.db.QueryRowContext(ctx, `SELECT resource_manifest FROM work_tasks WHERE task_id=$1`, cmd.TaskID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var pins []workInputPin
	if err = json.Unmarshal(raw, &pins); err != nil {
		return err
	}
	initial := cmd
	initial.ID = uuid.NewSHA1(task.ID, []byte(e.ID+":fixed-inputs")).String()
	if cmd.Submitted || (cmd.Turn > 1 && !task.Recovered) {
		return nil
	}
	inputs := taskflow.CreateTaskReq{ID: task.ID, CodingAgent: task.CodingAgent, Env: map[string]string{"JINGJIAAGENT_WORK_MODE": "1"}, Attachments: []taskflow.Attachment{}}
	if len(pins) > 0 {
		store, err := oss.NewS3Compatible(ctx, c.objectStorage, oss.S3Option{ForcePathStyle: c.objectStorage.ForcePathStyle})
		if err != nil {
			return err
		}
		for _, pin := range pins {
			var key, digest string
			var size int64
			if err = c.ledger.db.QueryRowContext(ctx, `SELECT storage_key,sha256,size FROM work_resource_versions WHERE id=$1`, pin.VersionID).Scan(&key, &digest, &size); err != nil {
				return err
			}
			if digest != pin.SHA256 || size > 32<<20 {
				return errors.New("初始资料校验失败或超过输入大小限制")
			}
			address, err := store.PresignGet(ctx, key, 15*time.Minute)
			if err != nil {
				return err
			}
			inputs.Attachments = append(inputs.Attachments, taskflow.Attachment{URL: address, Filename: pin.Filename, SHA256: pin.SHA256})
		}
	}
	if task.Recovered {
		task.RecoveryInputs, err = c.engine(e.NodeID).attachmentLocations(ctx, e.SandboxID, initial, inputs)
	} else {
		err = c.engine(e.NodeID).attachments(ctx, e.SandboxID, initial, inputs)
	}
	if err != nil {
		return err
	}
	return nil
}
