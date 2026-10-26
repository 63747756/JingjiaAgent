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
	c.preview.Mode, c.preview.BaseURL, c.preview.PortRange = "port", "http://192.0.2.10", "31000-31099"
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

func TestPreviewTaskManualOpenAndAIWhitelistBoundary(t *testing.T) {
	c, owner, task, _ := previewTaskFixture(t)
	ctx := context.Background()
	foreign := uuid.New()
	empty := []string{}
	invalid := []string{"192.0.2.31", "not-an-ip"}
	cidr := []string{"192.0.2.0/24"}
	manual := []string{"192.0.2.31", "::ffff:192.0.2.31", "192.0.2.32"}
	assertCounts := func(mappings, events, observations int) {
		t.Helper()
		var gotMappings, gotEvents, gotObservations int
		err := c.ledger.db.QueryRow(`SELECT
 (SELECT count(*) FROM runtime_port_forwards),
 (SELECT count(*) FROM runtime_events),
 (SELECT count(*) FROM runtime_preview_client_ips)`).Scan(&gotMappings, &gotEvents, &gotObservations)
		if err != nil || gotMappings != mappings || gotEvents != events || gotObservations != observations {
			t.Fatalf("unexpected side effects: mappings=%d events=%d observations=%d, err=%v", gotMappings, gotEvents, gotObservations, err)
		}
	}
	for _, tc := range []struct {
		name string
		user uuid.UUID
		port int
		ips  *[]string
	}{
		{"foreign explicit whitelist", foreign, 8080, &manual},
		{"anonymous", uuid.Nil, 8080, &manual},
		{"empty whitelist", owner, 8080, &empty},
		{"invalid whitelist", owner, 8080, &invalid},
		{"CIDR is not an IP", owner, 8080, &cidr},
		{"invalid port", owner, 65536, &manual},
		{"missing observed IP", owner, 8080, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.OpenTaskPreview(ctx, tc.user, task, tc.port, tc.ips); err == nil {
				t.Fatal("invalid management request succeeded")
			}
			assertCounts(0, 0, 0)
		})
	}
	// The MCP operation has no allowlist parameter and still needs a trusted
	// observation for its first mapping. Failed manual requests cannot supply one.
	if _, err := c.RequestPreview(ctx, owner, task, 8080); err == nil || !strings.Contains(err.Error(), "whitelist") {
		t.Fatal("AI request bypassed the observed-address requirement", err)
	}
	assertCounts(0, 0, 0)
	raw, err := c.OpenTaskPreview(ctx, owner, task, 8080, &manual)
	if err != nil {
		t.Fatal("owner cannot open with an explicit first whitelist", err)
	}
	opened := decodePreviewInfo(t, raw)
	if !opened.Success || opened.ForwardID == nil || opened.AccessURL == nil || strings.Join(opened.WhitelistIPs, ",") != "192.0.2.31,192.0.2.32" {
		t.Fatal("manual mapping did not normalize the whitelist", opened)
	}
	assertCounts(1, 1, 0)
	if _, err = c.OpenTaskPreview(ctx, foreign, task, 8080, &empty); err == nil {
		t.Fatal("foreign user overwrote an existing mapping")
	}
	if _, err = c.OpenTaskPreview(ctx, owner, task, 8080, &invalid); err == nil {
		t.Fatal("invalid whitelist overwrote an existing mapping")
	}
	assertCounts(1, 1, 0)
	// An AI request may reuse the owner-managed mapping without broadening it,
	// even after the employee's newly observed address changes.
	if err = c.RecordPreviewClientIP(ctx, owner, task, "192.0.2.99"); err != nil {
		t.Fatal(err)
	}
	raw, err = c.RequestPreview(ctx, owner, task, 8080)
	if err != nil {
		t.Fatal(err)
	}
	reused := decodePreviewInfo(t, raw)
	if reused.ForwardID == nil || *reused.ForwardID != *opened.ForwardID || strings.Join(reused.WhitelistIPs, ",") != "192.0.2.31,192.0.2.32" {
		t.Fatal("AI request replaced the explicit whitelist", reused)
	}
	assertCounts(1, 2, 1)
}

func TestPreviewTaskObservedIPFallbackAndOwnerScope(t *testing.T) {
	c, owner, task, env := previewTaskFixture(t)
	ctx := context.Background()
	if _, err := c.RequestPreview(ctx, owner, task, 8080); err == nil || !strings.Contains(err.Error(), "whitelist") {
		t.Fatal("missing trusted IP did not fail closed", err)
	}
	var mappings int
	if err := c.ledger.db.QueryRow("SELECT count(*) FROM runtime_port_forwards").Scan(&mappings); err != nil || mappings != 0 {
		t.Fatal("failed request allocated mapping", err, mappings)
	}
	for _, ip := range []string{"", "bad", "0.0.0.0", "::", "224.0.0.1", "ff02::1"} {
		if err := c.RecordPreviewClientIP(ctx, owner, task, ip); err == nil {
			t.Fatal("invalid observed IP saved", ip)
		}
	}
	if err := c.RecordPreviewClientIP(ctx, uuid.Nil, uuid.Nil, "192.0.2.1"); err == nil {
		t.Fatal("anonymous observed IP saved")
	}
	if err := c.RecordPreviewClientIP(ctx, owner, uuid.Nil, "::ffff:192.0.2.20"); err != nil {
		t.Fatal(err)
	}
	ips, err := c.previewEmployeeIPs(ctx, owner, task)
	if err != nil || len(ips) != 1 || ips[0] != "192.0.2.20" {
		t.Fatal("creation-time IP fallback failed", ips, err)
	}
	foreign := uuid.New()
	if err := c.RecordPreviewClientIP(ctx, foreign, task, "192.0.2.30"); err == nil {
		t.Fatal("foreign user replaced task IP")
	}
	if _, err := c.RequestPreview(ctx, foreign, task, 8080); err == nil {
		t.Fatal("foreign user opened preview")
	}
	if _, err := c.ListTaskPreviews(ctx, foreign, task, "foreign"); err == nil {
		t.Fatal("foreign user listed preview")
	}
	if err := c.RecordPreviewClientIP(ctx, owner, task, "192.0.2.21"); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordPreviewClientIP(ctx, owner, uuid.Nil, "192.0.2.22"); err != nil {
		t.Fatal(err)
	}
	ips, err = c.previewEmployeeIPs(ctx, owner, task)
	if err != nil || len(ips) != 1 || ips[0] != "192.0.2.21" {
		t.Fatal("another task's recent IP replaced task-specific observation", ips, err)
	}
	if err := c.RecordPreviewClientIP(ctx, owner, task, "192.0.2.23"); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := c.ledger.db.QueryRow("SELECT count(*) FROM runtime_preview_client_ips WHERE user_id=$1", owner).Scan(&rows); err != nil || rows != 2 {
		t.Fatal("observations not upserted", rows, err)
	}
	raw, err := c.RequestPreview(ctx, owner, task, 8080)
	if err != nil {
		t.Fatal(err)
	}
	info := decodePreviewInfo(t, raw)
	if info.AccessURL == nil || *info.AccessURL != "http://192.0.2.10:31000" || len(info.WhitelistIPs) != 1 || info.WhitelistIPs[0] != "192.0.2.23" {
		t.Fatal("wrong observed address or URL", info)
	}
	if _, err = c.ledger.db.Exec("UPDATE runtime_environments SET state='deleted' WHERE id=$1", env); err != nil {
		t.Fatal(err)
	}
	if _, err = c.RequestPreview(ctx, owner, task, 8080); err == nil {
		t.Fatal("deleted environment published")
	}
}

func TestPreviewTaskWhitelistStateAndEvents(t *testing.T) {
	c, owner, task, env := previewTaskFixture(t)
	ctx := context.Background()
	if err := c.RecordPreviewClientIP(ctx, owner, uuid.Nil, "192.0.2.20"); err != nil {
		t.Fatal(err)
	}
	raw, err := c.RequestPreview(ctx, owner, task, 8080)
	if err != nil {
		t.Fatal(err)
	}
	info := decodePreviewInfo(t, raw)
	if info.ForwardID == nil {
		t.Fatal("missing mapping identity")
	}
	list, err := c.ListTaskPreviews(ctx, owner, task, "preview-list-123")
	if err != nil || list.RequestId != "preview-list-123" || len(list.Ports) != 1 {
		t.Fatal("correlated list response failed", list, err)
	}
	encoded, _ := json.Marshal(list)
	if !strings.Contains(string(encoded), `"request_id":"preview-list-123"`) {
		t.Fatal("list response JSON loses request ID", string(encoded))
	}
	foreign := uuid.New()
	if _, err = c.UpdateTaskPreviewWhitelist(ctx, foreign, task, 8080, nil, true); err == nil {
		t.Fatal("foreign user edited whitelist")
	}
	if err = c.CloseTaskPreview(ctx, foreign, task, *info.ForwardID); err == nil {
		t.Fatal("foreign user closed preview")
	}
	if err = c.CloseTaskPreview(ctx, owner, task, *info.ForwardID); err != nil {
		t.Fatal(err)
	}
	if err = c.RecordPreviewClientIP(ctx, owner, task, "192.0.2.30"); err != nil {
		t.Fatal(err)
	}
	raw, err = c.UpdateTaskPreviewWhitelist(ctx, owner, task, 8080, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	updated := decodePreviewInfo(t, raw)
	if updated.Status != "closed" || updated.Success || updated.AccessURL != nil || strings.Join(updated.WhitelistIPs, ",") != "192.0.2.20,192.0.2.30" {
		t.Fatal("adding current IP reopened mapping or removed prior IP", updated)
	}
	empty := []string{}
	raw, err = c.UpdateTaskPreviewWhitelist(ctx, owner, task, 8080, &empty, false)
	if err != nil {
		t.Fatal(err)
	}
	updated = decodePreviewInfo(t, raw)
	if updated.Status != "closed" || len(updated.WhitelistIPs) != 0 {
		t.Fatal("empty whitelist did not deny all", updated)
	}
	if _, err = c.RequestPreview(ctx, owner, task, 8080); err == nil {
		t.Fatal("request overwrote explicit deny-all")
	}
	if _, err = c.UpdateTaskPreviewWhitelist(ctx, owner, task, 8080, nil, true); err != nil {
		t.Fatal(err)
	}
	raw, err = c.RequestPreview(ctx, owner, task, 8080)
	if err != nil {
		t.Fatal(err)
	}
	reopened := decodePreviewInfo(t, raw)
	if reopened.Status != "connected" || *reopened.ForwardID != *info.ForwardID || strings.Join(reopened.WhitelistIPs, ",") != "192.0.2.30" {
		t.Fatal("reopen changed mapping or whitelist", reopened)
	}
	raw, err = c.UpdateTaskPreviewWhitelist(ctx, owner, task, 8080, &empty, false)
	if err != nil {
		t.Fatal(err)
	}
	denied := decodePreviewInfo(t, raw)
	if denied.Success || denied.AccessURL == nil || !strings.Contains(denied.ErrorMessage, "whitelist is empty") {
		t.Fatal("deny-all still advertised as accessible", denied)
	}
	list, err = c.ListTaskPreviews(ctx, owner, task, "empty-list")
	if err != nil || len(list.Ports) != 1 || list.Ports[0].Success || !strings.Contains(list.Ports[0].ErrorMessage, "whitelist is empty") {
		t.Fatal("list lost deny-all status", list, err)
	}
	if _, err = c.UpdateTaskPreviewWhitelist(ctx, owner, task, 8080, nil, true); err != nil {
		t.Fatal(err)
	}
	if err = c.CloseTaskPreview(ctx, owner, task, *info.ForwardID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ledger.db.Exec("UPDATE runtime_environments SET state='hibernated' WHERE id=$1", env); err != nil {
		t.Fatal(err)
	}
	// Editing access settings neither requires nor resumes the background server.
	if _, err = c.UpdateTaskPreviewWhitelist(ctx, owner, task, 8080, &empty, false); err != nil {
		t.Fatal("stopped environment settings cannot be updated", err)
	}
	var state string
	if err = c.ledger.db.QueryRow("SELECT state FROM runtime_environments WHERE id=$1", env).Scan(&state); err != nil || state != "hibernated" {
		t.Fatal("whitelist edit woke environment", state, err)
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
			t.Fatal("invalid control stream event", string(raw))
		}
		var change map[string]string
		if err = json.Unmarshal(chunk.Data, &change); err != nil {
			t.Fatal(err)
		}
		if change["change_type"] != "PORT_CHANGE_TYPE_OPENED" && change["change_type"] != "PORT_CHANGE_TYPE_UPDATED" {
			t.Fatal("invalid preview refresh event", change)
		}
		events++
	}
	if err = rows.Err(); err != nil || events != 10 {
		t.Fatal("successful changes not persisted or failures emitted events", events, err)
	}
}
