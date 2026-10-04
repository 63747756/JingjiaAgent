package runtimeadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func liveAttachments(t *testing.T, ctx context.Context, c *Client, taskID uuid.UUID, vm, owner string, waitTurn func(int, string), output func(int) []byte) {
	liveAttachmentsAt(t, ctx, c, taskID, vm, owner, waitTurn, output, 18)
}

func liveAttachmentsAt(t *testing.T, ctx context.Context, c *Client, taskID uuid.UUID, vm, owner string, waitTurn func(int, string), output func(int) []byte, firstTurn int) {
	t.Helper()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	receipt := "ATTACHMENT_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	token := uuid.NewString()
	binary := bytes.Repeat([]byte{0, 128, 255, 3}, 512)
	files := map[string][]byte{"/text": []byte("中文附件随机回执：" + receipt + "\n"), "/binary": binary, "/empty": {}}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, exists := files[r.URL.Path]
		if r.Method != http.MethodGet || !exists || r.URL.Query().Get("token") != token {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	u.Host = "host.docker.internal:" + u.Port()
	selection := []taskflow.Attachment{}
	for _, file := range [][2]string{{"/text", "中文附件.txt"}, {"/binary", "二进制附件.bin"}, {"/empty", "空附件.txt"}} {
		selection = append(selection, taskflow.Attachment{URL: u.String() + file[0] + "?token=" + token, Filename: file[1]})
	}
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "读取本轮的 中文附件.txt，只回复其中的随机回执。不要使用 bash、MCP 或其他无关工具。", Attachments: selection}}))
	waitTurn(firstTurn, "complete")
	if !bytes.Contains(output(firstTurn), []byte(receipt)) || calls.Load() != 3 {
		t.Fatal("real Agent did not consume the current attachment bytes")
	}
	if bytes.Contains(output(firstTurn), []byte(token)) {
		t.Fatal("signed download credential reached Agent output")
	}
	var commandID string
	check(c.ledger.db.QueryRowContext(ctx, `SELECT id FROM runtime_commands WHERE task_id=$1 AND operation='task' AND turn=$2`, taskID, firstTurn).Scan(&commandID))
	root := "/workspace/.monkeycode/attachments/" + taskID.String() + "/" + commandID + "/"
	for _, file := range []struct {
		Name string
		Body []byte
	}{{"1-中文附件.txt", files["/text"]}, {"2-二进制附件.bin", binary}, {"3-空附件.txt", []byte{}}} {
		var content []byte
		check(c.FileManager().Download(ctx, taskflow.FileReq{ID: vm, UserID: owner, Path: root + file.Name}, func(_ uint64, data []byte) error { content = append(content, data...); return nil }))
		if !bytes.Equal(content, file.Body) {
			t.Fatal("attachment bytes changed inside sandbox")
		}
	}
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "仅回复 ATTACHMENT_CLEAR_OK，不要调用工具。"}}))
	waitTurn(firstTurn+1, "complete")
	if !bytes.Contains(output(firstTurn+1), []byte("ATTACHMENT_CLEAR_OK")) || calls.Load() != 3 {
		t.Fatal("attachment-free round failed or redownloaded old files")
	}
	env, err := c.ledger.EnvironmentForTask(ctx, taskID.String())
	check(err)
	var pointer struct {
		Files []json.RawMessage `json:"files"`
	}
	text, err := c.engines[env.NodeID].execute(ctx, env.SandboxID, "cat /data/state/monkeycode-attachments/"+taskID.String()+".json", 4096)
	check(err)
	check(json.Unmarshal([]byte(text), &pointer))
	if len(pointer.Files) != 0 {
		t.Fatal("attachment-free round retained previous selection")
	}
	t.Log("real Agent consumed an unpredictable Chinese attachment receipt; Unicode/binary/empty bytes matched, and a later round cleared its attachment selection without deleting prior files")
}
