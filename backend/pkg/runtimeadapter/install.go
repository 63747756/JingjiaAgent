package runtimeadapter

import (
	"context"
	"database/sql"
	"encoding/hex"
	"github.com/chaitin/MonkeyCode/backend/errcode"
	"github.com/google/uuid"
)

// Compare the instance actually deployed by the installer to the verified
// backend connection. A healthy URL pointing at a different machine is a failure.
func (c *Client) ConfirmNodeInstallation(ctx context.Context, id, instance, fingerprint string) (bool, error) {
	parsed, err := uuid.Parse(instance)
	decoded, he := hex.DecodeString(fingerprint)
	if err != nil || parsed == uuid.Nil || parsed.String() != instance || he != nil || len(decoded) != 32 {
		return false, errcode.ErrRuntimeInstallIdentity
	}
	var actualInstance, actualFingerprint sql.NullString
	err = c.ledger.db.QueryRowContext(ctx, `SELECT instance_id, fingerprint FROM runtime_nodes WHERE node_id=$1`, id).Scan(&actualInstance, &actualFingerprint)
	if err == sql.ErrNoRows || (err == nil && !actualInstance.Valid) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if actualInstance.String != instance || actualFingerprint.String != fingerprint {
		return false, errcode.ErrRuntimeInstallIdentity
	}
	return c.nodeReady(ctx, id)
}
