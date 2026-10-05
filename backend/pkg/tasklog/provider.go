package tasklog

import (
	"context"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

type Provider interface {
	Name() string
	QueryLatestTurn(ctx context.Context, taskID uuid.UUID, taskCreatedAt, end time.Time) (*QueryLatestTurnResp, error)
	QueryTurns(ctx context.Context, taskID uuid.UUID, taskCreatedAt time.Time, opts QueryTurnsOpts) (*QueryTurnsResp, error)
	QueryUserInputs(ctx context.Context, taskID uuid.UUID, taskCreatedAt time.Time, cursor string, limit int) (*QueryUserInputsResp, error)
}

// DurableStreamer snapshots history and its sequence watermark together.
// Delayed ingest cannot be discarded by a wall-clock timestamp cutoff.
type DurableStreamer interface {
	ReplayTask(context.Context, string) (*QueryLatestTurnResp, uint64, bool, error)
	TaskLiveAfter(context.Context, string, uint64, func(*taskflow.TaskChunk) error) error
}
