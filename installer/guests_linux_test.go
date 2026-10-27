//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeGuestDocker(t *testing.T, s *deployment) string {
	t.Helper()
	bin := t.TempDir()
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("GUEST_FIXTURES", bin)
	for _, id := range []string{"live", "stopped", "foreign"} {
		root := filepath.ToSlash(s.Data)
		if id == "foreign" {
			root += "-other"
		}
		fixture := fmt.Sprintf(`[{"Id":%q,"Config":{"Labels":{"agent-compose.sandbox_id":%q,"agent-compose.driver":"docker"}},"State":{"Running":%t},"Mounts":[{"Type":"bind","Source":%q,"Destination":"/workspace"}]}]`, id, "sandbox-"+id, id != "stopped", root+"/runtime/sandboxes/a/b/c/sandbox-"+id+"/workspace")
		if err := os.WriteFile(filepath.Join(bin, id+".json"), []byte(fixture), 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$GUEST_FIXTURES/calls"
case "$*" in
 "ps -q --filter name=agent-compose- --all") printf 'live\nstopped\nforeign\n';;
 "ps -q --filter name=agent-compose-") printf 'live\nforeign\n';;
 "inspect "*) cat "$GUEST_FIXTURES/$2.json";;
 "stop --time 30 live") ;;
 *) exit 1;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestGuestMaintenanceRestoresOnlyPreviouslyRunningOwnedContainers(t *testing.T) {
	s := &deployment{Root: t.TempDir(), Data: t.TempDir(), Project: "test", Mode: "single"}
	bin := fakeGuestDocker(t, s)
	if err := suspendGuests(s); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, "suspended-guests.json")
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "[\n  \"live\"\n]\n" {
		t.Fatalf("unexpected maintenance ledger: %q %v", b, err)
	}
	// A stale foreign/deleted entry and duplicates cannot expand recovery scope.
	if err := writeJSON(path, []string{"live", "foreign", "deleted", "live"}); err != nil {
		t.Fatal(err)
	}
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ SandboxID string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, body.SandboxID+":"+filepath.Base(r.URL.Path))
		status := "RUNNING"
		if len(calls) == 1 {
			status = "STOPPED" // daemon stale while the original Docker container is live
		}
		fmt.Fprintf(w, `{"sandbox":{"sandboxId":%q,"driver":"docker","status":"SANDBOX_STATUS_%s"}}`, body.SandboxID, status)
	}))
	defer server.Close()
	if err := resumeGuestsUsing(s, func() (*guestRuntimeClient, error) {
		return &guestRuntimeClient{client: server.Client(), baseURL: server.URL}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "sandbox-live:GetSandbox,sandbox-live:ResumeSandbox,sandbox-live:GetSandbox" {
		t.Fatalf("incorrect recovery scope: %v", calls)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("successful recovery ledger not cleared")
	}
	log, _ := os.ReadFile(filepath.Join(bin, "calls"))
	if strings.Contains(string(log), "start ") || strings.Contains(string(log), "restart ") {
		t.Fatalf("runtime lifecycle bypassed: %s", log)
	}
}

func TestGuestResumeFailurePreservesLedger(t *testing.T) {
	for _, failure := range []string{"connect", "rpc", "stopped", "container-stopped", "identity"} {
		t.Run(failure, func(t *testing.T) {
			s := &deployment{Root: t.TempDir(), Data: t.TempDir(), Project: "test", Mode: "single"}
			bin := fakeGuestDocker(t, s)
			path := filepath.Join(s.Root, "suspended-guests.json")
			if err := writeJSON(path, []string{"live"}); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			if failure == "identity" || failure == "container-stopped" {
				fixture := filepath.Join(bin, "live.json")
				b, _ := os.ReadFile(fixture)
				old, new := `"agent-compose.driver":"docker"`, `"agent-compose.driver":"other"`
				if failure == "container-stopped" {
					old, new = `"Running":true`, `"Running":false`
				}
				_ = os.WriteFile(fixture, []byte(strings.ReplaceAll(string(b), old, new)), 0600)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if failure == "rpc" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				status := "RUNNING"
				if failure == "stopped" {
					status = "STOPPED"
				}
				fmt.Fprintf(w, `{"sandbox":{"sandboxId":"sandbox-live","driver":"docker","status":"SANDBOX_STATUS_%s"}}`, status)
			}))
			defer server.Close()
			err := resumeGuestsUsing(s, func() (*guestRuntimeClient, error) {
				if failure == "connect" {
					return nil, fmt.Errorf("runtime unavailable")
				}
				return &guestRuntimeClient{client: server.Client(), baseURL: server.URL}, nil
			})
			after, readErr := os.ReadFile(path)
			if err == nil || readErr != nil || string(before) != string(after) {
				t.Fatalf("failed restore lost ledger: err=%v ledger=%q read=%v", err, after, readErr)
			}
		})
	}
}

func TestGuestResumeWithoutLedgerDoesNotContactRuntime(t *testing.T) {
	if err := resumeGuestsUsing(&deployment{Root: t.TempDir()}, func() (*guestRuntimeClient, error) {
		t.Fatal("runtime contacted without a maintenance ledger")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
}
