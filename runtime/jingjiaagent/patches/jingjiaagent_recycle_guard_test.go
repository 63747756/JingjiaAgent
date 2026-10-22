package sandboxes_test

import (
	"context"
	"errors"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"testing"
	"time"
)

func TestJingjiaAgentRecycleGuardRunsBeforeDeletionIntent(t *testing.T) {
	root := t.TempDir()
	sandbox := removalTestSandbox(t, removalTestSandboxSpec{Root: root, ID: "guarded", Status: domain.VMStatusStopped, Updated: time.Now()})
	store := &removalTestStore{sandboxes: map[string]*domain.Sandbox{sandbox.Summary.ID: sandbox}}
	runtime := &removalTestRuntime{}
	coordinator := &sandboxes.RemovalCoordinator{SandboxRoot: root, Store: store, Runtime: runtime}
	want := errors.New("uncheckpointed source files")
	ctx := sandboxes.WithJingjiaAgentRecycleGuard(context.Background(), func(*domain.Sandbox) error { return want })
	if _, err := coordinator.Remove(ctx, sandbox.Summary.ID, false); !errors.Is(err, want) {
		t.Fatal(err)
	}
	record, err := sandboxes.ReadOwnershipRecord(root, sandbox.Summary.ID)
	if err != nil || record.LifecycleState == "deleting" || runtime.removeCalls != 0 || store.removeCalls != 0 {
		t.Fatalf("guard persisted deletion: %#v %v", record, err)
	}
	if _, err := coordinator.Remove(context.Background(), sandbox.Summary.ID, false); err != nil {
		t.Fatal(err)
	}
}
