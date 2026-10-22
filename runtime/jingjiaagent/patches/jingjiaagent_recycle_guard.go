package sandboxes

import (
	"context"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type jingjiaAgentRecycleGuardKey struct{}

// The callback runs inside RemovalCoordinator's lifecycle lock, before it
// persists deletion intent. Retried deletions use the existing durable intent.
func WithJingjiaAgentRecycleGuard(ctx context.Context, check func(*domain.Sandbox) error) context.Context {
	return context.WithValue(ctx, jingjiaAgentRecycleGuardKey{}, check)
}

func jingjiaAgentRecycleGuard(ctx context.Context, sandbox *domain.Sandbox) error {
	if check, ok := ctx.Value(jingjiaAgentRecycleGuardKey{}).(func(*domain.Sandbox) error); ok {
		return check(sandbox)
	}
	return nil
}
