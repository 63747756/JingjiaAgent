package runtimeadapter

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

func configFixtureFile(name, content string) taskflow.ConfigFile {
	return taskflow.ConfigFile{Path: "/data/home/.codingmatrix/project-tpl/.ai-ready/rules/" + name, Content: content}
}

func TestConfigBatchUsesOneExecForMultipleFiles(t *testing.T) {
	client, env, server, lookups := nativeModelFixture(t)
	mode := uint32(0640)
	configs := []taskflow.ConfigFile{configFixtureFile("platform.md", "中文规则"), configFixtureFile("team.md", "team rules")}
	configs[1].Mode = &mode
	lookups(1)
	if err := (&fileClient{client}).writeConfigs(context.Background(), env, configs); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(server.operations, []string{"configs"}) || len(server.files) != len(configs) {
		t.Fatalf("small configurations were not batched: operations=%v, files=%d", server.operations, len(server.files))
	}
	for _, config := range configs {
		if string(server.files[config.Path]) != config.Content {
			t.Fatal("configuration content changed during batching")
		}
	}
	if configs[0].Mode != nil || *configs[1].Mode != mode {
		t.Fatal("batching changed the accepted configuration")
	}
}

func TestConfigBatchRemoteFailureDoesNotFallback(t *testing.T) {
	client, env, server, lookups := nativeModelFixture(t)
	server.batchError = connect.NewError(connect.CodeUnavailable, errors.New("lost batch response"))
	lookups(1)
	err := (&fileClient{client}).writeConfigs(context.Background(), env, []taskflow.ConfigFile{configFixtureFile("rule.md", "new")})
	if connect.CodeOf(err) != connect.CodeUnavailable || !reflect.DeepEqual(server.operations, []string{"configs"}) {
		t.Fatalf("uncertain remote batch was replayed: err=%v operations=%v", err, server.operations)
	}
}

func TestConfigBatchChecksOwnerAndSandboxBeforeExec(t *testing.T) {
	for _, changed := range []string{"owner", "sandbox"} {
		t.Run(changed, func(t *testing.T) {
			client, env, server, lookups := nativeModelFixture(t)
			lookups(1)
			if changed == "owner" {
				env.OwnerID = "another-owner"
			} else {
				env.SandboxID = "previous-sandbox"
			}
			if err := (&fileClient{client}).writeConfigs(context.Background(), env, []taskflow.ConfigFile{configFixtureFile("rule.md", "new")}); err == nil || len(server.operations) != 0 {
				t.Fatalf("changed %s passed configuration authorization: err=%v operations=%v", changed, err, server.operations)
			}
		})
	}
}

func largestBatchContent(t *testing.T) int {
	t.Helper()
	mode := uint32(0600)
	low, high := 0, maxGuestFileCommandBytes
	for low < high {
		size := (low + high + 1) / 2
		file := configFixtureFile("rule.md", strings.Repeat("x", size))
		file.Mode = &mode
		command, err := guestFileCommand(map[string]any{"op": "configs", "files": []taskflow.ConfigFile{file}})
		if err == nil {
			if len(command) > maxGuestFileCommandBytes {
				t.Fatal("accepted oversized encoded Guest command")
			}
			low = size
		} else if errors.Is(err, errGuestFileRequestTooLarge) {
			high = size - 1
		} else {
			t.Fatal(err)
		}
	}
	if low == 0 || low >= maxGuestFileCommandBytes {
		t.Fatal("embedded script and encoding were not included in the command limit")
	}
	return low
}

func TestConfigBatchEncodedCommandLimitFallsBackBeforeExec(t *testing.T) {
	limit := largestBatchContent(t)
	for _, size := range []int{limit, limit + 1} {
		client, env, server, lookups := nativeModelFixture(t)
		file := configFixtureFile("rule.md", strings.Repeat("x", size))
		if size == limit {
			lookups(1)
		} else {
			lookups(2) // Batch authorization, then the original streaming upload.
		}
		if err := (&fileClient{client}).writeConfigs(context.Background(), env, []taskflow.ConfigFile{file}); err != nil {
			t.Fatal(err)
		}
		if string(server.files[file.Path]) != file.Content || len(server.pending) != 0 {
			t.Fatal("large configuration was not preserved by the fallback")
		}
		if size == limit && !reflect.DeepEqual(server.operations, []string{"configs"}) {
			t.Fatalf("largest safe batch was not submitted as one call: %v", server.operations)
		}
		if size > limit && (server.operations[0] != "begin" || server.operations[len(server.operations)-1] != "commit") {
			t.Fatalf("oversized batch reached the Guest instead of streaming: %v", server.operations)
		}
	}
}

func TestConfigBatchFileCountLimitUsesStreamingUpload(t *testing.T) {
	client, env, server, lookups := nativeModelFixture(t)
	files := make([]taskflow.ConfigFile, maxConfigBatchFiles+1)
	for i := range files {
		files[i] = configFixtureFile("rule.md", "same rule")
	}
	lookups(len(files) + 1)
	if err := (&fileClient{client}).writeConfigs(context.Background(), env, files); err != nil {
		t.Fatal(err)
	}
	if len(server.operations) != 3*len(files) || string(server.files[files[0].Path]) != files[0].Content {
		t.Fatalf("file count fallback failed: %v", server.operations)
	}
	for _, op := range server.operations {
		if op == "configs" {
			t.Fatal("oversized file-count batch was sent to Guest")
		}
	}
}
