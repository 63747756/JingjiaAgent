//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeLifecycleDocker(t *testing.T, services, query string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	t.Setenv("CALL_LOG", log)
	t.Setenv("FIXTURE_SERVICES", services)
	t.Setenv("FIXTURE_QUERY", query)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$CALL_LOG"
case "$*" in
 *"ps --status running --services") printf '%s\n' "$FIXTURE_SERVICES";;
 *"ps -aq gateway") printf 'gateway-id\n';;
 *"ps -aq backend") printf 'backend-id\n';;
 *"exec -T postgres psql"*)
   [ "$FIXTURE_QUERY" != error ] || exit 1
   printf '%s\n' "$FIXTURE_QUERY";;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return log
}

func TestStopIsIdempotentWhenAlreadyStopped(t *testing.T) {
	log := fakeLifecycleDocker(t, "", "error")
	s := &deployment{Root: t.TempDir(), Data: t.TempDir(), Project: "test"}
	for i := 0; i < 2; i++ {
		if err := stop(s); err != nil {
			t.Fatal(err)
		}
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "psql") || strings.Contains(string(calls), " start") || strings.Contains(string(calls), " stop") {
		t.Fatalf("already stopped services were mutated: %s", calls)
	}
}

func TestStopPartialOutageDoesNotStartOtherServices(t *testing.T) {
	for _, query := range []string{"1", "error"} {
		t.Run(query, func(t *testing.T) {
			log := fakeLifecycleDocker(t, "postgres\ngateway", query)
			s := &deployment{Root: t.TempDir(), Data: t.TempDir(), Project: "test"}
			if stop(s) == nil {
				t.Fatal("unsafe shutdown accepted")
			}
			calls, _ := os.ReadFile(log)
			if !strings.Contains(string(calls), "start gateway-id") || strings.Contains(string(calls), "start backend") || strings.Contains(string(calls), "start gateway\n") {
				t.Fatalf("did not restore only the previously running ingress: %s", calls)
			}
		})
	}
}

func TestStopWithoutLedgerLeavesPartialOutageUnchanged(t *testing.T) {
	log := fakeLifecycleDocker(t, "gateway\nbackend", "error")
	if stop(&deployment{Root: t.TempDir(), Data: t.TempDir(), Project: "test"}) == nil {
		t.Fatal("stopped without checking ledger")
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), " stop") || strings.Contains(string(calls), " start") {
		t.Fatalf("partial outage mutated: %s", calls)
	}
}

func TestStopChecksLedgerAfterProducerShutdown(t *testing.T) {
	log := fakeLifecycleDocker(t, "postgres\ngateway\nbackend", "0")
	if err := stop(&deployment{Root: t.TempDir(), Data: t.TempDir(), Project: "test"}); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	text := string(calls)
	if strings.Count(text, "psql") != 2 || strings.LastIndex(text, "stop backend") > strings.LastIndex(text, "psql") || !strings.HasSuffix(text, " stop\n") {
		t.Fatalf("shutdown did not fence producers before the final check: %s", text)
	}
}
