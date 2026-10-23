package runtimeadapter

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/runtimenode"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

// Reconciled after every heartbeat, so a restart between account creation and
// node registration does not strand the first installation. Existing records,
// including disabled/revoked ones, are never overwritten.
func (c *Client) ensureDeploymentNode(ctx context.Context) error {
	d := c.deployment
	if !d.Enabled || !d.LocalNode {
		return nil
	}
	var owner, team string
	err := c.ledger.db.QueryRowContext(ctx, `SELECT user_id,team_id FROM team_members WHERE role='admin' ORDER BY created_at LIMIT 1`).Scan(&owner, &team)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("jingjiaagent-local-node:"+team)).String()
	if _, err = c.nodeStore.Get(ctx, id); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	token, err := os.ReadFile(d.LocalNodeTokenFile)
	if err != nil {
		return err
	}
	ca, err := os.ReadFile(d.LocalNodeCAFile)
	if err != nil {
		return err
	}
	record := runtimenode.Record{Config: config.RuntimeNode{ID: id, Name: "本机工作空间", URL: d.LocalNodeURL, OwnerID: owner, TeamID: team, GuestImage: d.GuestImage}, Material: runtimenode.Material{Token: strings.TrimSpace(string(token)), CA: string(ca)}, Name: "本机工作空间", Accepting: true, Phase: "ready", Imported: true}
	if err = c.nodeStore.Create(ctx, record); err != nil {
		var p *pq.Error
		if !errors.As(err, &p) || p.Code != "23505" {
			return err
		}
	}
	return c.RefreshNodes(ctx)
}
