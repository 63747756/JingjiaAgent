package repo

import (
	"context"
	"github.com/chaitin/MonkeyCode/backend/pkg/entx"
	"github.com/google/uuid"
)

func (h *HostRepo) RejectPreparedRuntimeCreation(ctx context.Context, owner uuid.UUID, id string) error {
	return entx.RejectPreparedRuntimeCreation(ctx, h.db, owner, id)
}
