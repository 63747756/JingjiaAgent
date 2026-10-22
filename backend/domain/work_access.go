package domain

import (
	"context"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

// WorkAccessGuard rechecks current membership before exposing or continuing a
// private work task. The execution service never treats cached session teams as
// an authorization decision.
type WorkAccessGuard interface {
	CheckWorkTaskAccess(context.Context, uuid.UUID, uuid.UUID, bool) error
}

// RoundSkillResolver has no dependency on task execution, avoiding a DI cycle.
type RoundSkillResolver interface {
	ResolveRoundSkills(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID) (*taskflow.RoundSkillSnapshot, *taskflow.SkillMention, error)
}

// RoundResourceValidator checks the whole selection before a message is accepted.
type RoundResourceValidator interface {
	ValidateRoundResources(context.Context, *taskflow.CreateTaskReq) error
}
