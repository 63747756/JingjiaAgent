package taskflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// RestartBusinessMutation is server-created metadata describing a business
// change already authorized by the task usecase. The durable runtime stores it
// with its encrypted command and commits it with the restart's terminal state.
// It must never be populated from a public request's arbitrary JSON fields.
type RestartBusinessMutation struct {
	OwnerID           uuid.UUID                 `json:"owner_id"`
	ModelSwitch       *RestartModelSwitch       `json:"model_switch,omitempty"`
	ResourceSelection *RestartResourceSelection `json:"resource_selection,omitempty"`
}

type RestartModelSwitch struct {
	ID      uuid.UUID `json:"id"`
	ModelID uuid.UUID `json:"model_id"`
}

type RestartResourceSelection struct {
	SkillIDs  []string `json:"skill_ids"`
	PluginIDs []string `json:"plugin_ids"`
}

// RestartPendingError means admission succeeded (or its commit is uncertain).
// The durable worker owns the result; a disconnected HTTP caller must not turn
// this into a failed business mutation. Unwrap preserves context cancellation.
type RestartPendingError struct {
	Err error
}

func (e *RestartPendingError) Error() string {
	return fmt.Sprintf("restart is pending durable reconciliation: %v", e.Err)
}

func (e *RestartPendingError) Unwrap() error { return e.Err }

func IsRestartPending(err error) bool {
	var pending *RestartPendingError
	return errors.As(err, &pending)
}

// RestartResumer resumes a previously admitted operation without regenerating
// credentials or execution configuration from today's task state. found=false
// permits normal first admission; found=true transfers result ownership to the
// original durable command, including when waiting returns an error.
// Only ID, RequestId, LoadSession and BusinessMutation are supplied by callers.
type RestartResumer interface {
	ResumeRestart(ctx context.Context, request RestartTaskReq) (response *RestartTaskResp, found bool, err error)
}
