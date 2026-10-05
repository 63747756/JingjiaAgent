package sandboxes_test

import (
	"context"
	"errors"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJingjiaAgentRecycleReceiptProvesCompletedRemoval(t *testing.T) {
	root := t.TempDir()
	id := "receipt-owned"
	sandbox := removalTestSandbox(t, removalTestSandboxSpec{Root: root, ID: id, Status: domain.VMStatusRunning, Updated: time.Now()})
	store := &removalTestStore{sandboxes: map[string]*domain.Sandbox{id: sandbox}}
	runtime := &removalTestRuntime{}
	c := &sandboxes.RemovalCoordinator{SandboxRoot: root, Store: store, Runtime: runtime}
	if _, err := c.Remove(context.Background(), "never-owned", true); !errors.Is(err, sandboxes.ErrOwnershipUnknown) {
		t.Fatal("missing ownership acknowledged")
	}
	if result, err := c.Remove(context.Background(), id, true); err != nil || !result.Removed {
		t.Fatal("initial removal failed", err)
	}
	// Recreate coordinator: only its permanent receipt proves a prior removal.
	c = &sandboxes.RemovalCoordinator{SandboxRoot: root, Store: store, Runtime: runtime}
	if result, err := c.Remove(context.Background(), id, true); err != nil || !result.Removed {
		t.Fatal("lost reply not reconciled", err)
	}
	if runtime.removeCalls != 1 {
		t.Fatal("receipt triggered duplicate runtime removal")
	}
	// A recreated sandbox takes precedence over an old receipt for that ID.
	again := removalTestSandbox(t, removalTestSandboxSpec{Root: root, ID: id, Status: domain.VMStatusRunning, Updated: time.Now()})
	store.sandboxes[id] = again
	if _, err := c.Remove(context.Background(), id, true); err != nil {
		t.Fatal(err)
	}
	if runtime.removeCalls != 2 {
		t.Fatal("old receipt skipped a recreated runtime")
	}
	receipt := filepath.Join(root, ".jingjiaagent-recycled", id+".json")
	if err := os.WriteFile(receipt, []byte(`{"version":1,"sandbox_id":"different","removed":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Remove(context.Background(), id, true); !errors.Is(err, sandboxes.ErrOwnershipUnknown) {
		t.Fatal("mismatched receipt accepted")
	}
	if err := os.WriteFile(receipt, []byte(`invalid`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Remove(context.Background(), id, true); !errors.Is(err, sandboxes.ErrOwnershipUnknown) {
		t.Fatal("corrupt receipt accepted")
	}
}
