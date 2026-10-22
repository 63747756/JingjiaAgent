package work

import (
	"github.com/63747756/jingjiaagent/backend/biz/agentresource"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/samber/do"
)

func ProvideWork(i *do.Injector) {
	do.Provide(i, NewRepository)
	do.Provide(i, func(i *do.Injector) (domain.WorkAccessGuard, error) {
		return &AccessGuard{store: do.MustInvoke[*Repository](i)}, nil
	})
	do.Provide(i, NewService)
	do.Provide(i, func(i *do.Injector) (domain.RoundSkillResolver, error) {
		return &SkillResolver{store: do.MustInvoke[*Repository](i), ent: do.MustInvoke[*db.Client](i), objects: do.MustInvoke[agentresource.ObjectStore](i)}, nil
	})
	do.Provide(i, NewHandler)
}
func InvokeWork(i *do.Injector) { do.MustInvoke[*Handler](i) }
