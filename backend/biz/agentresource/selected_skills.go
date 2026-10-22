package agentresource

import (
	"context"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/agentskill"
)

// AuthorizedSkills preserves identities: name collisions are resolved by the
// work selection UI, not by silently shadowing a requested resource.
func AuthorizedSkills(ctx context.Context, client *db.Client, scope ScopeFilter) ([]*db.AgentSkill, error) {
	r := &repoImpl{db: client}
	q := client.AgentSkill.Query().Where(agentskill.IsDeletedEQ(false), agentskill.IsOrphanEQ(false))
	rows, err := applyScopeSkill(q, scope).All(ctx)
	if err != nil {
		return nil, err
	}
	return r.filterSkillGroups(ctx, rows, scope)
}
