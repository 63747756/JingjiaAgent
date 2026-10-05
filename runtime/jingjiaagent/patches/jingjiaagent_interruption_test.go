package runs

import (
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestJingjiaAgentInterruptedRunStopsWithoutRemovingVolumes(t *testing.T) {
	run := domain.ProjectRunRecord{CleanupPolicy: domain.ProjectRunCleanupKeepRunning, SandboxID: "sbx-owned", SandboxCreated: true}
	if jingjiaAgentInterruptedCleanupAction(run) != domain.ProjectRunCompletionActionNone {
		t.Fatal("unrelated Run policy changed")
	}
	run.Labels = map[string]string{"jingjiaagent_command": "f475fe27-5372-4b6b-aef8-1e5952cedf4e", "jingjiaagent_environment": "9a96d21f-a172-4f3f-8a1c-e86d4e7a0ac3"}
	if jingjiaAgentInterruptedCleanupAction(run) != domain.ProjectRunCompletionActionStop {
		t.Fatal("interrupted owned Guest was not fenced")
	}
	run.Labels["jingjiaagent_environment"] = "agent_9a96d21f-a172-4f3f-8a1c-e86d4e7a0ac3"
	if jingjiaAgentInterruptedCleanupAction(run) != domain.ProjectRunCompletionActionStop {
		t.Fatal("product environment ID did not fence interrupted Guest")
	}
	run.Labels["jingjiaagent_environment"] = "invalid"
	if jingjiaAgentInterruptedCleanupAction(run) != domain.ProjectRunCompletionActionNone {
		t.Fatal("invalid origin changed cleanup")
	}
}
