package runtimeadapter

import (
	"context"
	"errors"
	"testing"
)

func TestAutomaticReviewGateOnlyAffectsNewComposeTasks(t *testing.T) {
	for _, backend := range []string{"taskflow", "agent_compose"} {
		c := &Client{backend: backend}
		err := c.CheckNewReview(context.Background())
		if errors.Is(err, ErrReviewDeferred) != (backend == "agent_compose") {
			t.Fatalf("automatic review admission changed for %s: %v", backend, err)
		}
	}
}
