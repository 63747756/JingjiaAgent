package runtimeadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/asseturl"
	"github.com/63747756/jingjiaagent/backend/pkg/oss"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func TestAttachmentHistoryKeepsAuthenticatedBrowserURLs(t *testing.T) {
	owner := uuid.New()
	cfg := config.ObjectStorageConfig{Enabled: true, Endpoint: "http://storage:9000", AccessEndpoint: "https://web.example.com/oss",
		AgentAccessEndpoint: "http://storage.internal:9000", AccessKey: "key", AccessKeySecret: "secret", Bucket: "private", TempPrefix: "temp"}
	storeCfg := cfg
	storeCfg.AccessEndpoint = cfg.AgentAccessEndpoint
	store, err := oss.NewS3Compatible(context.Background(), storeCfg, oss.S3Option{ForcePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	key := "temp/" + owner.String() + "_中文附件.txt"
	signed, err := store.PresignGet(context.Background(), key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{objectStorage: cfg}
	input := []taskflow.Attachment{{URL: signed, Filename: "中文附件.txt"}, {URL: "https://external.example/attachment?token=provided-by-user", Filename: "external.txt"}}
	got := c.publicAttachments(owner.String(), input)
	if got[0].URL != asseturl.Build(key) || got[1] != input[1] || input[0].URL != signed {
		t.Fatal("browser projection changed source files or external URLs")
	}
	if c.publicAttachments(uuid.NewString(), input)[0].URL != signed {
		t.Fatal("a foreign asset was reinterpreted as owned")
	}
	for _, changed := range []string{strings.Replace(signed, "storage.internal", "storage.internal.attacker", 1), strings.Replace(signed, "/private/", "/private-other/", 1), strings.Replace(signed, "X-Amz-Algorithm=AWS4-HMAC-SHA256", "X-Amz-Algorithm=other", 1)} {
		if c.publicAttachments(owner.String(), []taskflow.Attachment{{URL: changed}})[0].URL != changed {
			t.Fatal("unrelated download URL was rewritten")
		}
	}
	raw := mustJSON(map[string]any{"content": []byte("read attachment"), "attachments": input, "legacy_field": true})
	projected := c.publicInputData(owner.String(), raw)
	if bytes.Contains(projected, []byte("X-Amz-")) {
		t.Fatal("internal signature leaked into browser history")
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(projected, &data) != nil || string(data["legacy_field"]) != "true" {
		t.Fatal("history fields changed")
	}
	chunk := &taskflow.TaskChunk{Event: "user-input", Data: raw, Seq: 41, Timestamp: 12345}
	err = c.publicInputCallback(owner.String(), func(view *taskflow.TaskChunk) error {
		if view.Seq != chunk.Seq || view.Timestamp != chunk.Timestamp || bytes.Contains(view.Data, []byte("X-Amz-")) {
			t.Fatal("stream projection broke replay identity")
		}
		return nil
	})(chunk)
	if err != nil || !bytes.Equal(chunk.Data, raw) {
		t.Fatal("stream projection mutated persisted input")
	}
}

func TestAttachmentHistoryRecognizesObjectStoreEndpointLayouts(t *testing.T) {
	owner := uuid.New()
	key := "temp/" + owner.String() + "_中文空格 附件.txt"
	for _, test := range []struct {
		name, endpoint string
		pathStyle      bool
	}{
		{"path style", "http://storage.internal:9000", true},
		{"proxy prefix", "https://storage.internal/oss", true},
		{"explicit bucket path", "https://storage.internal/oss/private", true},
		{"virtual host bucket", "https://private.storage.internal", false},
		{"virtual host proxy prefix", "https://private.storage.internal/oss", false},
		{"whitespace", " https://private.storage.internal/oss/ ", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.ObjectStorageConfig{Enabled: true, Endpoint: "http://storage:9000", AgentAccessEndpoint: test.endpoint,
				AccessKey: "key", AccessKeySecret: "secret", Bucket: "private", TempPrefix: "temp", ForcePathStyle: test.pathStyle}
			store, err := oss.NewS3Compatible(context.Background(), cfg, oss.S3Option{ForcePathStyle: test.pathStyle})
			if err != nil {
				t.Fatal(err)
			}
			signed, err := store.WithAccessEndpoint(test.endpoint).PresignGet(context.Background(), key, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			c := &Client{objectStorage: cfg}
			actual := c.publicAttachments(owner.String(), []taskflow.Attachment{{URL: signed, Filename: "中文空格 附件.txt"}})
			if actual[0].URL != asseturl.Build(key) {
				t.Fatal("object-store URL was not projected to authenticated browser history")
			}
		})
	}
}
