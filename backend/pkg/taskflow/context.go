package taskflow

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ContextUsage describes the latest request, never accumulated billing tokens.
type ContextUsage struct {
	Used             *uint64 `json:"used"`
	Size             int     `json:"size"`
	Estimated        bool    `json:"estimated"`
	UpdatedAt        int64   `json:"updated_at"`
	RequestStartedAt int64   `json:"request_started_at,omitempty"`
}

type ContextUsageReader interface {
	TaskContextUsage(context.Context, uuid.UUID) (*ContextUsage, error)
}

type ContextUsageRecorder interface {
	RecordContextUsage(context.Context, string, int, uint64, time.Time) error
}
