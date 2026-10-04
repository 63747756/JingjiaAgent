package repo

import (
	"context"

	"github.com/chaitin/MonkeyCode/backend/db"
	"github.com/google/uuid"
)

// AddDefaultGroupHost is shared by first-time runtime enrollment and the
// original installer. Callers must already authorize the owner and team.
func AddDefaultGroupHost(ctx context.Context, tx *db.Tx, teamID uuid.UUID, hostID string) error {
	return addDefaultGroupHost(ctx, tx, teamID, hostID)
}
