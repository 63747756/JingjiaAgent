package vmstatus

import (
	"time"

	etypes "github.com/63747756/jingjiaagent/backend/ent/types"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

const readyTimeout = 3 * time.Minute

type Input struct {
	Online bool
	// Optional live runtime observation, never an Agent-reported condition.
	RuntimeStatus taskflow.VirtualMachineStatus
	Conditions    []*etypes.Condition
	IsRecycled    bool
	CreatedAt     time.Time
	Now           time.Time
}

func Resolve(input Input) taskflow.VirtualMachineStatus {
	if input.IsRecycled {
		return taskflow.VirtualMachineStatusOffline
	}
	switch input.RuntimeStatus {
	case taskflow.VirtualMachineStatusOnline, taskflow.VirtualMachineStatusOffline,
		taskflow.VirtualMachineStatusHibernated, taskflow.VirtualMachineStatusPending:
		return input.RuntimeStatus
	}

	if input.Online {
		return taskflow.VirtualMachineStatusOnline
	}

	if cs := input.Conditions; len(cs) > 0 {
		last := cs[len(cs)-1]
		switch last.Type {
		case etypes.ConditionTypeFailed:
			return taskflow.VirtualMachineStatusOffline

		case etypes.ConditionTypeHibernated:
			return taskflow.VirtualMachineStatusHibernated
		}
	}

	if input.Now.Sub(input.CreatedAt) > readyTimeout {
		return taskflow.VirtualMachineStatusOffline
	}

	return taskflow.VirtualMachineStatusPending
}
