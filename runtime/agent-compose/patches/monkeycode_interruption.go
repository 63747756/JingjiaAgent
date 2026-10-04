package runs

import (
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/google/uuid"
)

// A daemon crash loses execution handles. Fence the owned Guest before
// publishing failure, so an orphaned command cannot overlap a new user turn.
// Stop retains workspace/state volumes; it never removes or replays a Run.
func monkeyCodeInterruptedCleanupAction(run domain.ProjectRunRecord) string {
	if run.CleanupPolicy == domain.ProjectRunCleanupKeepRunning && strings.TrimSpace(run.SandboxID) != "" {
		command, commandErr := uuid.Parse(run.Labels["monkeycode_command"])
		// Product environments use agent_<uuid>; list summaries omit these
		// labels entirely and StageInterrupted reloads the durable detail.
		environment, environmentErr := uuid.Parse(strings.TrimPrefix(run.Labels["monkeycode_environment"], "agent_"))
		if commandErr == nil && command != uuid.Nil && environmentErr == nil && environment != uuid.Nil {
			return domain.ProjectRunCompletionActionStop
		}
	}
	return CompletionCleanupAction(run.CleanupPolicy, strings.TrimSpace(run.SandboxID) != "", run.SandboxCreated)
}
