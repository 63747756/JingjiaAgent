package taskflow

import (
	"context"

	"github.com/google/uuid"
)

// NextTurnModelSelector persists a selection without contacting the runtime.
// Already accepted commands retain their immutable execution configuration.
type NextTurnModelSelector interface {
	SelectNextTurnModel(context.Context, NextTurnModelReq) error
}

type NextTurnModelReq struct {
	TaskID    uuid.UUID
	OwnerID   uuid.UUID
	ModelID   uuid.UUID
	RequestID string
	Config    *TaskExecutionConfig
}
