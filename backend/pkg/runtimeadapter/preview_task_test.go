package runtimeadapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"github.com/google/uuid"
)

func previewTaskFixture(t *testing.T) (*Client, uuid.UUID, uuid.UUID, string) {
	t.Helper()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	t.Cleanup(app.Close)
	mux := http.NewServeMux()
	installPreviewTunnel(t, mux, app.URL)
	path, handler := rpc.NewExecServiceHandler(&previewExecFixture{})
	mux.Handle(path, handler)
	c, env, owner := previewFixture(t, testEngine(t, mux))
	c.preview.Mode, c.preview.BaseURL = "domain", "http://preview.example.net"
	task := uuid.New()
	if _, err := c.ledger.db.Exec("INSERT INTO tasks(id,status) VALUES($1,'running')", task); err != nil {
		t.Fatal(err)
	}
	if err := c.ledger.Stage(context.Background(), taskflow.CreateTaskReq{ID: task, VMID: env, CodingAgent: taskflow.CodingAgentOpenCode, Text: "preview task"}); err != nil {
		t.Fatal(err)
	}
	return c, uuid.MustParse(owner), task, env
}

func decodePreviewInfo(t *testing.T, raw json.RawMessage) taskflow.PortForwardInfo {
	t.Helper()
	var info taskflow.PortForwardInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	return info
}

func TestPreviewTaskPublicOpenAndAIPolicyBoundary(t *testing.T) {
	c, owner, task, _ := previewTaskFixture(t)
	ctx := context.Background()
	foreign := uuid.New()
	assertCounts := func(mappings, events int) {
		t.Helper()
		var gotMappings, gotEvents int
		err := c.ledger.db.QueryRow(`SELECT (SELECT count(*) FROM runtime_port_forwards),(SELECT count(*) FROM runtime_events)`).Scan(&gotMappings, &gotEvents)
		if err != nil || gotMappings != mappings || gotEvents != events {
			t.Fatalf("unexpected side effects: mappings=%d events=%d, err=%v", gotMappings, gotEvents, err)
		}
	}
	for _, tc := range []struct {
		name string
		user uuid.UUID
		port int
	}{
		{"foreign user", foreign, 8080}, {"anonymous", uuid.Nil, 8080}, {"invalid port", owner, 65536},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.OpenTaskPreview(ctx, tc.user, task, tc.port, false); err == nil {
				t.Fatal("invalid management request succeeded")
			}
			assertCounts(0, 0)
		})
	}
	raw, err := c.OpenTaskPreview(ctx, owner, task, 8080, false)
	if err != nil {
		t.Fatal(err)
	}
	opened := decodePreviewInfo(t, raw)
	if !opened.Success || opened.ForwardID == nil || opened.AccessURL == nil || opened.AccessMode != "public" || len(opened.WhitelistIPs) != 0 {
		t.Fatal("first visit did not create a public route", opened)
	}
	assertCounts(1, 1)
	for _, ips := range [][]string{{"bad"}, {"192.0.2.0/24"}} {
		if _, err = c.UpdateTaskPreviewWhitelist(ctx, owner, task, 8080, "ip_allowlist", &ips, ""); err == nil {
			t.Fatal("invalid IP accepted")
		}
	}
	manual := []string{"192.0.2.31", "::ffff:192.0.2.31", "192.0.2.32"}
	if _, err = c.UpdateTaskPreviewWhitelist(ctx, foreign, task, 8080, "ip_allowlist", &manual, ""); err == nil {
		t.Fatal("foreign user changed access policy")
	}
	if _, err = c.UpdateTaskPreviewWhitelist(ctx, owner, task, 8080, "invalid", &manual, ""); err == nil {
		t.Fatal("invalid access mode accepted")
	}
	assertCounts(1, 1)
	raw, err = c.UpdateTaskPreviewWhitelist(ctx, owner, task, 8080, "ip_allowlist", &manual, "")
	if err != nil {
		t.Fatal(err)
	}
	restricted := decodePreviewInfo(t, raw)
	if restricted.AccessMode != "ip_allowlist" || strings.Join(restricted.WhitelistIPs, ",") != "192.0.2.31,192.0.2.32" {
		t.Fatal("whitelist was not normalized", restricted)
	}
	raw, err = c.RequestPreview(ctx, owner, task, 8080)
	if err != nil {
		t.Fatal(err)
	}
	reused := decodePreviewInfo(t, raw)
	if reused.ForwardID == nil || *reused.ForwardID != *opened.ForwardID || reused.AccessMode != "ip_allowlist" || strings.Join(reused.WhitelistIPs, ",") != "192.0.2.31,192.0.2.32" {
		t.Fatal("AI request changed saved policy", reused)
	}
	assertCounts(1, 3)
	if _, err = c.RequestPreview(ctx, foreign, task, 8080); err == nil {
		t.Fatal("foreign user requested preview")
	}
	if _, err = c.ListTaskPreviews(ctx, foreign, task, "foreign"); err == nil {
		t.Fatal("foreign user listed preview")
	}
	assertCounts(1, 3)
}

func TestPreviewTaskUsesCurrentConnectionIP(t *testing.T) {
	c, owner, task, _ := previewTaskFixture(t)
	ctx := context.Background()
	if _, err := c.RequestPreview(ctx, owner, task, 8080); err != nil {
		t.Fatal("first AI request unexpectedly required a stored employee IP", err)
	}
	initial := []string{"192.0.2.20"}
	raw, err := c.UpdateTaskPreviewWhitelist(ctx, owner, task, 8080, "ip_allowlist", &initial, "::ffff:192.0.2.21")
	if err != nil {
		t.Fatal(err)
	}
	info := decodePreviewInfo(t, raw)
	if strings.Join(info.WhitelistIPs, ",") != "192.0.2.20,192.0.2.21" {
		t.Fatal("current connection IP was not normalized and appended", info)
	}
	raw, err = c.UpdateTaskPreviewWhitelist(ctx, owner, task, 8080, "ip_allowlist", nil, "192.0.2.22")
	if err != nil {
		t.Fatal(err)
	}
	info = decodePreviewInfo(t, raw)
	if strings.Join(info.WhitelistIPs, ",") != "192.0.2.20,192.0.2.21,192.0.2.22" {
		t.Fatal("a later connection did not use its own IP", info)
	}
	for _, ip := range []string{"bad", "0.0.0.0", "::", "224.0.0.1", "ff02::1"} {
		if _, err = c.UpdateTaskPreviewWhitelist(ctx, owner, task, 8080, "ip_allowlist", nil, ip); err == nil {
			t.Fatal("invalid current connection IP accepted", ip)
		}
	}
}

func TestPreviewTaskPolicyStateAndEvents(t *testing.T) {
	c, owner, task, env := previewTaskFixture(t)
	ctx := context.Background()
	raw, err := c.RequestPreview(ctx, owner, task, 8080)
	if err != nil {
		t.Fatal(err)
	}
	info := decodePreviewInfo(t, raw)
	if info.ForwardID == nil {
		t.Fatal("missing route identity")
	}
	expectedEvents := 1
	list, err := c.ListTaskPreviews(ctx, owner, task, "preview-list-123")
	if err != nil || list.RequestId != "preview-list-123" || len(list.Ports) != 1 {
		t.Fatal("correlated list failed", list, err)
	}
	encoded, _ := json.Marshal(list)
	if !strings.Contains(string(encoded), `"request_id":"preview-list-123"`) {
		t.Fatal("request ID lost", string(encoded))
	}
	if err = c.CloseTaskPreview(ctx, uuid.New(), task, *info.ForwardID); err == nil {
		t.Fatal("foreign user closed route")
	}
	if err = c.CloseTaskPreview(ctx, owner, task, *info.ForwardID); err != nil {
		t.Fatal(err)
	}
	expectedEvents++
	raw, err = c.UpdateTaskPreviewWhitelist(ctx, owner, task, 8080, "ip_allowlist", nil, "192.0.2.30")
	if err != nil {
		t.Fatal(err)
	}
	expectedEvents++
	updated := decodePreviewInfo(t, raw)
	if updated.Status != "closed" || updated.Success || updated.AccessURL != nil || strings.Join(updated.WhitelistIPs, ",") != "192.0.2.30" {
		t.Fatal("policy update reopened closed route", updated)
	}
	if _, err = c.RequestPreview(ctx, owner, task, 8080); err == nil {
		t.Fatal("AI reopened closed route")
	}
	if _, err = c.OpenTaskPreview(ctx, owner, task, 8080, false); err == nil {
		t.Fatal("stale visit reopened closed route without explicit action")
	}
	raw, err = c.OpenTaskPreview(ctx, owner, task, 8080, true)
	if err != nil {
		t.Fatal(err)
	}
	expectedEvents++
	reopened := decodePreviewInfo(t, raw)
	if !reopened.Success || *reopened.ForwardID != *info.ForwardID || reopened.AccessMode != "ip_allowlist" || strings.Join(reopened.WhitelistIPs, ",") != "192.0.2.30" {
		t.Fatal("explicit reopen changed route or policy", reopened)
	}
	empty := []string{}
	raw, err = c.UpdateTaskPreviewWhitelist(ctx, owner, task, 8080, "ip_allowlist", &empty, "")
	if err != nil {
		t.Fatal(err)
	}
	expectedEvents++
	denied := decodePreviewInfo(t, raw)
	if denied.Success || denied.AccessURL == nil || !strings.Contains(denied.ErrorMessage, "whitelist is empty") {
		t.Fatal("empty restricted policy did not deny all", denied)
	}
	raw, err = c.RequestPreview(ctx, owner, task, 8080)
	if err != nil {
		t.Fatal(err)
	}
	expectedEvents++
	denied = decodePreviewInfo(t, raw)
	if denied.Success || denied.AccessMode != "ip_allowlist" || len(denied.WhitelistIPs) != 0 {
		t.Fatal("AI broadened deny-all policy", denied)
	}
	list, err = c.ListTaskPreviews(ctx, owner, task, "deny-all")
	if err != nil || len(list.Ports) != 1 || list.Ports[0].Success {
		t.Fatal("deny-all lost from service list", list, err)
	}
	raw, err = c.UpdateTaskPreviewWhitelist(ctx, owner, task, 8080, "public", &empty, "")
	if err != nil {
		t.Fatal(err)
	}
	expectedEvents++
	if public := decodePreviewInfo(t, raw); !public.Success || public.AccessMode != "public" {
		t.Fatal("public mode incorrectly requires an IP", public)
	}
	if err = c.CloseTaskPreview(ctx, owner, task, *info.ForwardID); err != nil {
		t.Fatal(err)
	}
	expectedEvents++
	if _, err = c.ledger.db.Exec("UPDATE runtime_environments SET state='hibernated' WHERE id=$1", env); err != nil {
		t.Fatal(err)
	}
	if _, err = c.UpdateTaskPreviewWhitelist(ctx, owner, task, 8080, "ip_allowlist", &empty, ""); err != nil {
		t.Fatal("policy edit failed for stopped workspace", err)
	}
	expectedEvents++
	var state string
	if err = c.ledger.db.QueryRow("SELECT state FROM runtime_environments WHERE id=$1", env).Scan(&state); err != nil || state != "hibernated" {
		t.Fatal("policy edit woke workspace", state, err)
	}
	if _, err = c.RequestPreview(ctx, owner, task, 8080); err == nil {
		t.Fatal("request published hibernated workspace")
	}
	rows, err := c.ledger.db.Query("SELECT chunk FROM runtime_events WHERE task_id=$1 ORDER BY seq", task)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	events := 0
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var chunk taskflow.TaskChunk
		if err = json.Unmarshal(raw, &chunk); err != nil {
			t.Fatal(err)
		}
		if chunk.Event != "task-event" || chunk.Kind != "port_change" {
			t.Fatal("invalid event", string(raw))
		}
		var change map[string]string
		if err = json.Unmarshal(chunk.Data, &change); err != nil {
			t.Fatal(err)
		}
		if change["change_type"] != "PORT_CHANGE_TYPE_OPENED" && change["change_type"] != "PORT_CHANGE_TYPE_UPDATED" {
			t.Fatal("invalid event type", change)
		}
		events++
	}
	if err = rows.Err(); err != nil || events != expectedEvents {
		t.Fatal("successful changes missing or failed changes emitted events", events, expectedEvents, err)
	}
}
