package agentresource

import (
	"context"

	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/agentskill"
	"github.com/google/uuid"
)

// MandatorySkillIDs includes enabled, authorized force-delivery skills even
// when their active version is absent. Work admission must reject that broken
// dependency rather than losing it in the active-version listing projection.
func MandatorySkillIDs(ctx context.Context, client *db.Client, scope ScopeFilter) ([]uuid.UUID, error) {
	r := &repoImpl{db: client}
	q := client.AgentSkill.Query().Where(agentskill.IsDeletedEQ(false), agentskill.IsOrphanEQ(false))
	rows, err := applyScopeSkill(q, scope).All(ctx)
	if err != nil {
		return nil, err
	}
	rows, err = r.filterSkillGroups(ctx, rows, scope)
	if err != nil {
		return nil, err
	}
	ids := []uuid.UUID{}
	for _, s := range pickSkillByName(rows) {
		if s.Enabled && s.IsForceDelivery {
			ids = append(ids, s.ID)
		}
	}
	return ids, nil
}
