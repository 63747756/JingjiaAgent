package taskflow

import (
	"context"
	"errors"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
)

type RejectedCreationCleaner interface {
	RejectPreparedRuntimeCreation(context.Context, uuid.UUID, string) error
}

// These errors from VirtualMachiner.Create prove no environment was committed.
func IsCapacityRejection(err error) bool {
	return errors.Is(err, errcode.ErrRuntimeCapacityExhausted) || errors.Is(err, errcode.ErrRuntimeCapacityUnavailable) || errors.Is(err, errcode.ErrRuntimeResources)
}
