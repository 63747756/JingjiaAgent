package runs_test

import (
	"context"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
)

func TestMonkeyCodeInterruptedListSummaryReloadsDurableOwnership(t *testing.T) {
	ctx := context.Background()
	store := newCompletionTestStore(t)
	run := createCompletionTestRun(t, store, domain.ProjectRunRecord{
		RunID: "interrupted-run", ProjectID: "project-1", AgentName: "worker", AgentID: "agent-1",
		Status: domain.ProjectRunStatusRunning, SandboxID: "sandbox-1", CleanupPolicy: domain.ProjectRunCleanupKeepRunning,
		Labels: map[string]string{"monkeycode_command": "f475fe27-5372-4b6b-aef8-1e5952cedf4e", "monkeycode_environment": "agent_9a96d21f-a172-4f3f-8a1c-e86d4e7a0ac3"},
	})
	// ConfigStore list APIs intentionally return summaries without labels.
	run.Labels = nil
	manager := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: store})
	if err := manager.StageInterrupted(ctx, run, "daemon interrupted"); err != nil {
		t.Fatal(err)
	}
	journal, err := store.GetProjectRunCompletion(ctx, run.RunID)
	if err != nil || journal.CleanupAction != domain.ProjectRunCompletionActionStop {
		t.Fatalf("durable ownership was not loaded: action=%q err=%v", journal.CleanupAction, err)
	}
	if err := manager.StageInterrupted(ctx, domain.ProjectRunRecord{RunID: "unknown-run"}, "daemon interrupted"); err == nil {
		t.Fatal("unknown Run was assigned interrupted cleanup")
	}
}
