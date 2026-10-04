package taskflow

import "context"

// ReviewAdmissionGate controls new automatic review tasks only. Existing
// environments keep their recorded backend and continue to use TaskManager.
type ReviewAdmissionGate interface {
	CheckNewReview(context.Context) error
}
