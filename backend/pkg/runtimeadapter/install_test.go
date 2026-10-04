package runtimeadapter

import (
	"context"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestInstallConfirmationRequiresBoundIdentityAndLocalVerification(t *testing.T) {
	l := testLedger(t)
	ctx := context.Background()
	id := uuid.NewString()
	snapshot := validNodeSnapshot()
	c := &Client{ledger: l}
	if ready, err := c.ConfirmNodeInstallation(ctx, id, snapshot.InstanceID, snapshot.Fingerprint); ready || err != nil {
		t.Fatal("unregistered node confirmed", err)
	}
	if err := l.bindNode(ctx, id, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := l.saveNode(ctx, id, snapshot); err != nil {
		t.Fatal(err)
	}
	if ready, err := c.ConfirmNodeInstallation(ctx, id, snapshot.InstanceID, snapshot.Fingerprint); ready || err != nil {
		t.Fatal("shared database readiness bypassed local connection verification")
	}
	c.verifyNode(id, true)
	if ready, err := c.ConfirmNodeInstallation(ctx, id, snapshot.InstanceID, snapshot.Fingerprint); !ready || err != nil {
		t.Fatal("correct verified install not confirmed", err)
	}
	for _, input := range [][2]string{{uuid.NewString(), snapshot.Fingerprint}, {snapshot.InstanceID, strings.Repeat("b", 64)}, {"invalid", snapshot.Fingerprint}} {
		if _, err := c.ConfirmNodeInstallation(ctx, id, input[0], input[1]); err == nil {
			t.Fatal("wrong installation identity accepted")
		}
	}
	c.verifyNode(id, false)
	if ready, _ := c.ConfirmNodeInstallation(ctx, id, snapshot.InstanceID, snapshot.Fingerprint); ready {
		t.Fatal("failed local connection still confirmed")
	}
}
