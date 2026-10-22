package runtimeadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/oss"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

func (c *Client) refreshWorkResources(ctx context.Context, task *taskflow.CreateTaskReq) error {
	if !c.ledger.workEnabled {
		return nil
	}
	var raw []byte
	var err error
	if task.RoundSkills != nil {
		raw, err = json.Marshal(task.RoundSkills.Pins)
	} else {
		// Existing queued commands retain their pre-upgrade snapshot.
		err = c.ledger.db.QueryRowContext(ctx, `SELECT skill_manifest FROM work_tasks WHERE task_id=$1`, task.ID).Scan(&raw)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var pins []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Key     string `json:"s3_key"`
		SHA256  string `json:"sha256"`
	}
	if err = json.Unmarshal(raw, &pins); err != nil {
		return err
	}
	resources := taskflow.AgentResources{}
	if task.AgentResources != nil {
		resources = *task.AgentResources
	}
	resources.Skills = nil
	if len(pins) == 0 && len(resources.Plugins) == 0 {
		task.AgentResources = &resources
		return nil
	}
	storage, err := oss.NewS3Compatible(ctx, c.objectStorage, oss.S3Option{ForcePathStyle: c.objectStorage.ForcePathStyle})
	if err != nil {
		return err
	}
	for _, pin := range pins {
		if len(pin.SHA256) != 64 || pin.Key == "" {
			return errors.New("work skill pin is invalid")
		}
		address, err := storage.PresignGet(ctx, pin.Key, 15*time.Minute)
		if err != nil {
			return err
		}
		resources.Skills = append(resources.Skills, &taskflow.AgentResourceAssetRef{Name: pin.Name, Version: pin.Version, SHA256: pin.SHA256, ZipURL: address})
	}
	// Plugin packages are pinned at admission just like Skills. Renew only the
	// download capability, never the accepted version or digest. This also runs
	// for rounds without Skills and retries after a long queue/hibernation.
	plugins := make([]*taskflow.AgentResourceAssetRef, 0, len(resources.Plugins))
	for _, ref := range resources.Plugins {
		if ref == nil || ref.S3Key == "" {
			return errors.New("work plugin pin is invalid")
		}
		address, err := storage.PresignGet(ctx, ref.S3Key, 15*time.Minute)
		if err != nil {
			return err
		}
		renewed := *ref
		renewed.ZipURL = address
		plugins = append(plugins, &renewed)
	}
	resources.Plugins = plugins
	task.AgentResources = &resources
	return nil
}

func (c *Client) refreshRestoreWorkspace(ctx context.Context, e *Environment, cmd Command) error {
	if cmd.Operation != "restore_prepare" || cmd.Submitted {
		return nil
	}
	var cid, key, digest string
	if err := c.ledger.db.QueryRowContext(ctx, `SELECT checkpoint_id::text,archive_key,payload_sha256 FROM runtime_restores WHERE task_id=$1 AND new_environment_id=$2 AND state='preparing'`, cmd.TaskID, e.ID).Scan(&cid, &key, &digest); err != nil {
		return err
	}
	if key == "" || len(digest) != 64 {
		return errors.New("restore manifest invalid")
	}
	store, err := oss.NewS3Compatible(ctx, c.objectStorage, oss.S3Option{ForcePathStyle: c.objectStorage.ForcePathStyle})
	if err != nil {
		return err
	}
	address, err := store.PresignGet(ctx, key, 15*time.Minute)
	if err != nil {
		return err
	}
	e.Request.ZipUrl = address + "#jingjiaagent-checkpoint=" + cid + "&expected_sha256=" + digest
	return nil
}
