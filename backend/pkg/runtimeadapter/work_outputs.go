package runtimeadapter

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/63747756/jingjiaagent/backend/pkg/oss"
	"time"
)

//go:embed guest/formal_outputs.py
var formalOutputScript []byte

func (c *Client) hydrateCanonicalOutputs(ctx context.Context, e Environment) error {
	if !c.ledger.workEnabled || !workEnvironment(e) {
		return nil
	}
	rows, err := c.ledger.db.QueryContext(ctx, `SELECT a.source_path,v.storage_key,v.sha256,v.size,r.sha256 FROM work_artifacts a JOIN work_artifact_versions v ON v.id=a.current_version_id JOIN work_artifact_versions r ON r.id=v.source_version_id WHERE a.task_id=$1 AND a.deleted_at IS NULL AND v.validation_status='passed'`, e.Request.TaskID)
	if err != nil {
		return err
	}
	type output struct {
		Path         string `json:"path"`
		Key          string `json:"-"`
		SHA256       string `json:"sha256"`
		Size         int64  `json:"size"`
		SourceSHA256 string `json:"source_sha256"`
		URL          string `json:"url"`
	}
	files := []output{}
	for rows.Next() {
		var f output
		if err = rows.Scan(&f.Path, &f.Key, &f.SHA256, &f.Size, &f.SourceSHA256); err != nil {
			rows.Close()
			return err
		}
		files = append(files, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(files) == 0 {
		return err
	}
	store, err := oss.NewS3Compatible(ctx, c.objectStorage, oss.S3Option{ForcePathStyle: c.objectStorage.ForcePathStyle})
	if err != nil {
		return err
	}
	for i := range files {
		files[i].URL, err = store.PresignGet(ctx, files[i].Key, 15*time.Minute)
		if err != nil {
			return err
		}
	}
	data := mustJSON(map[string]any{"files": files})
	script := "python3 -c \"import base64;exec(base64.b64decode('" + base64.StdEncoding.EncodeToString(formalOutputScript) + "'))\" '" + base64.StdEncoding.EncodeToString(data) + "'"
	if len(script) > 120000 {
		return errors.New("canonical outputs exceed request limit")
	}
	text, err := c.engine(e.NodeID).execute(ctx, e.SandboxID, script, 4096)
	if err != nil {
		return err
	}
	var result struct {
		Error string `json:"error"`
		Data  struct {
			Installed bool `json:"installed"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(text), &result) != nil || result.Error != "" || !result.Data.Installed {
		return errors.New("正式成果刷新失败")
	}
	return nil
}
