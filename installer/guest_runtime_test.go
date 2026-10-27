package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGuestRuntimeResume(t *testing.T) {
	for _, tc := range []struct {
		name, initial, resume, final, reclaim, retained string
		failMethod, wantCalls                           string
		wantError, wrongID                              bool
	}{
		{name: "stopped", initial: "STOPPED", resume: "RUNNING", final: "RUNNING", retained: "retained", wantCalls: "GetSandbox,ResumeSandbox,GetSandbox"},
		{name: "running retry preserves live services", initial: "RUNNING", final: "RUNNING", wantCalls: "GetSandbox,GetSandbox"},
		{name: "get failure", failMethod: "GetSandbox", wantCalls: "GetSandbox", wantError: true},
		{name: "resume failure", initial: "STOPPED", failMethod: "ResumeSandbox", wantCalls: "GetSandbox,ResumeSandbox", wantError: true},
		{name: "resume not running", initial: "STOPPED", resume: "STOPPED", wantCalls: "GetSandbox,ResumeSandbox", wantError: true},
		{name: "final status not running", initial: "STOPPED", resume: "RUNNING", final: "STOPPED", wantCalls: "GetSandbox,ResumeSandbox,GetSandbox", wantError: true},
		{name: "pending is not resumed", initial: "PENDING", wantCalls: "GetSandbox", wantError: true},
		{name: "identity mismatch", initial: "STOPPED", wrongID: true, wantCalls: "GetSandbox", wantError: true},
		{name: "reclaimed is not rebuilt", initial: "STOPPED", reclaim: "WORKSPACE_RECLAMATION_STATE_RECLAIMED", wantCalls: "GetSandbox", wantError: true},
		{name: "released runtime is not rebuilt", initial: "STOPPED", retained: "released", wantCalls: "GetSandbox", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method := strings.TrimPrefix(r.URL.Path, "/agentcompose.v2.SandboxService/")
				calls = append(calls, method)
				var body struct{ SandboxID string }
				if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer private-test-token" ||
					r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Connect-Protocol-Version") != "1" ||
					json.NewDecoder(r.Body).Decode(&body) != nil || body.SandboxID != "sandbox-test" {
					t.Error("invalid private runtime request")
				}
				if method == tc.failMethod {
					http.Error(w, `{"message":"private-test-token must not be logged"}`, http.StatusPreconditionFailed)
					return
				}
				status := tc.initial
				if method == "ResumeSandbox" {
					status = tc.resume
				} else if len(calls) > 1 {
					status = tc.final
				}
				id := "sandbox-test"
				if tc.wrongID {
					id = "another-sandbox"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"sandbox": map[string]string{
					"sandboxId": id, "driver": "docker", "status": "SANDBOX_STATUS_" + status,
					"workspaceReclamationState": tc.reclaim, "stoppedRuntimeState": tc.retained,
				}})
			}))
			defer server.Close()
			client := &guestRuntimeClient{client: server.Client(), baseURL: server.URL, token: "private-test-token"}
			err := client.resume("sandbox-test")
			if (err != nil) != tc.wantError || strings.Join(calls, ",") != tc.wantCalls {
				t.Fatalf("error=%v calls=%v", err, calls)
			}
			if err != nil && strings.Contains(err.Error(), "private-test-token") {
				t.Fatal("credential leaked into runtime error")
			}
		})
	}
}

func TestGuestRuntimeDoesNotAcceptInvalidResponse(t *testing.T) {
	for _, response := range []string{`{}`, `<html>gateway unavailable</html>`, `{"sandbox":{"sandboxId":"sandbox-test","driver":"other","status":"SANDBOX_STATUS_RUNNING"}}`} {
		t.Run(fmt.Sprint(len(response)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, response) }))
			defer server.Close()
			client := &guestRuntimeClient{client: server.Client(), baseURL: server.URL}
			if client.resume("sandbox-test") == nil {
				t.Fatal("invalid runtime response accepted")
			}
		})
	}
}
