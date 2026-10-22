package runtimeadapter

import (
	"context"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

func resourceRefreshClient() *Client {
	return &Client{ledger: &Ledger{workEnabled: true}, objectStorage: config.ObjectStorageConfig{
		Enabled: true, Endpoint: "http://storage:9000", AccessEndpoint: "http://storage.internal:9000",
		AccessKey: "test-key", AccessKeySecret: "test-secret", Bucket: "private", ForcePathStyle: true,
	}}
}

func TestWorkResourcesRenewPluginURLWithoutChangingPinnedVersion(t *testing.T) {
	for _, withSkills := range []bool{false, true} {
		t.Run(map[bool]string{false: "plugin-only", true: "skills-and-plugins"}[withSkills], func(t *testing.T) {
			c := resourceRefreshClient()
			old := "https://old-storage.test/private/plugins/plugin/v1.zip?X-Amz-Date=20200101T000000Z&X-Amz-Expires=86400&X-Amz-Signature=expired"
			ref := &taskflow.AgentResourceAssetRef{Name: "plugin", Version: "v1", ZipURL: old,
				S3Key: "plugins/plugin/v1.zip", SHA256: strings.Repeat("a", 64), EntryFilename: "index.js"}
			task := taskflow.CreateTaskReq{
				RoundSkills: &taskflow.RoundSkillSnapshot{},
				AgentResources: &taskflow.AgentResources{
					Plugins: []*taskflow.AgentResourceAssetRef{ref},
					Skills:  []*taskflow.AgentResourceAssetRef{{Name: "stale"}},
				},
			}
			if withSkills {
				task.RoundSkills.Pins = []taskflow.RoundSkillPin{{
					Name: "skill", Version: "v2", S3Key: "skills/skill/v2.zip", SHA256: strings.Repeat("b", 64),
				}}
			}
			if err := c.refreshWorkResources(context.Background(), &task); err != nil {
				t.Fatal(err)
			}
			got := *task.AgentResources.Plugins[0]
			address, err := url.Parse(got.ZipURL)
			if err != nil {
				t.Fatal(err)
			}
			signedAt, err := time.Parse("20060102T150405Z", address.Query().Get("X-Amz-Date"))
			if err != nil || time.Since(signedAt) > time.Minute || address.Host != "storage.internal:9000" ||
				address.Path != "/private/"+ref.S3Key || address.Query().Get("X-Amz-Expires") != "900" || address.Query().Get("X-Amz-Signature") == "" {
				t.Fatalf("plugin did not receive a fresh bounded URL: %v", err)
			}
			got.ZipURL = old
			if !reflect.DeepEqual(got, *ref) || ref.ZipURL != old {
				t.Fatal("renewal changed the accepted plugin identity or mutated the persisted reference")
			}
			if len(task.AgentResources.Skills) != len(task.RoundSkills.Pins) {
				t.Fatal("stale skill selection survived refresh")
			}
			if withSkills && (task.AgentResources.Skills[0].Name != "skill" || task.AgentResources.Skills[0].Version != "v2") {
				t.Fatal("skill pin changed during plugin renewal")
			}
		})
	}
}

func TestWorkResourceRefreshRejectsMissingPluginPinAtomically(t *testing.T) {
	c := resourceRefreshClient()
	valid := &taskflow.AgentResourceAssetRef{Name: "valid", S3Key: "plugins/valid.zip", ZipURL: "old"}
	for _, invalid := range []*taskflow.AgentResourceAssetRef{nil, {Name: "invalid", ZipURL: "https://untrusted.test/plugin.zip"}} {
		resources := &taskflow.AgentResources{
			Skills:  []*taskflow.AgentResourceAssetRef{{Name: "original"}},
			Plugins: []*taskflow.AgentResourceAssetRef{valid, invalid},
		}
		task := taskflow.CreateTaskReq{RoundSkills: &taskflow.RoundSkillSnapshot{}, AgentResources: resources}
		if err := c.refreshWorkResources(context.Background(), &task); err == nil {
			t.Fatal("missing immutable plugin key was accepted")
		}
		if task.AgentResources != resources || valid.ZipURL != "old" || task.AgentResources.Skills[0].Name != "original" {
			t.Fatal("failed renewal partially changed resource selection")
		}
	}
}
