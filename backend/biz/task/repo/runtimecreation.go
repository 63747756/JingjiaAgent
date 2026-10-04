package repo

import (
	"context"
	"github.com/chaitin/MonkeyCode/backend/pkg/entx"
	"github.com/google/uuid"
)

func (t *TaskRepo) RejectPreparedRuntimeCreation(ctx context.Context, owner uuid.UUID, id string) error {
	return entx.RejectPreparedRuntimeCreation(ctx, t.db, owner, id)
}
