package runtimeadapter

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func assertContinuationSlots(t *testing.T, c *Client, command string, want int) {
	t.Helper()
	var count int
	if err := c.ledger.db.QueryRow(`SELECT count(*) FROM work_run_slots WHERE command_id=$1`, command).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("command has %d execution slots, want %d", count, want)
	}
}

func TestWaitingAndBlockedContinuationsReleaseAdmission(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "waiting", true: "blocked"}[blocked], func(t *testing.T) {
			c, req, owner := continuationFixture(t)
			ctx := context.Background()
			if _, err := c.ledger.db.Exec(`UPDATE teams SET task_total_concurrency_limit=1`); err != nil {
				t.Fatal(err)
			}
			cmd := acceptedContinuation(t, c, req, "next round")
			if err := c.acquireWorkSlot(ctx, cmd); err != nil {
				t.Fatal(err)
			}
			if err := c.waitContinuation(ctx, cmd, "preparing", "preparation failed", blocked, nil); err != nil {
				t.Fatal(err)
			}
			assertContinuationSlots(t, c, cmd.ID, 0)
			if _, err := c.ledger.db.Exec(`UPDATE runtime_commands SET available_at=now()+interval '1 hour' WHERE id=$1`, cmd.ID); err != nil {
				t.Fatal(err)
			}
			var profile uuid.UUID
			if err := c.ledger.db.QueryRow(`SELECT profile_id FROM work_tasks WHERE task_id=$1`, req.ID).Scan(&profile); err != nil {
				t.Fatal(err)
			}
			other := workRuntimeTask(t, c.ledger, profile)
			next, err := c.ledger.Claim(ctx)
			if err != nil || next.TaskID != other.ID.String() {
				t.Fatalf("another task could not prepare: %+v %v", next, err)
			}
			if err := c.acquireWorkSlot(ctx, next); err != nil {
				t.Fatalf("non-running continuation blocked another task: %v", err)
			}
			if blocked {
				if err := c.RetryContinuation(ctx, owner, req.ID, uuid.MustParse(cmd.ID), nil, false); err != nil {
					t.Fatal(err)
				}
				retry, err := c.ledger.Claim(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if err := c.acquireWorkSlot(ctx, retry); !errors.Is(err, errWorkQueued) {
					t.Fatalf("retry bypassed the now-full admission limit: %v", err)
				}
			}
		})
	}
}

func TestBlockedSlotsFromEarlierVersionAreReclaimed(t *testing.T) {
	c, req, _ := continuationFixture(t)
	ctx := context.Background()
	if _, err := c.ledger.db.Exec(`UPDATE teams SET task_total_concurrency_limit=1`); err != nil {
		t.Fatal(err)
	}
	cmd := acceptedContinuation(t, c, req, "old blocked round")
	if err := c.acquireWorkSlot(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ledger.db.Exec(`UPDATE runtime_commands SET state='blocked',lease_token=NULL,lease_until=NULL WHERE id=$1`, cmd.ID); err != nil {
		t.Fatal(err)
	}
	var profile uuid.UUID
	if err := c.ledger.db.QueryRow(`SELECT profile_id FROM work_tasks WHERE task_id=$1`, req.ID).Scan(&profile); err != nil {
		t.Fatal(err)
	}
	workRuntimeTask(t, c.ledger, profile)
	next, err := c.ledger.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.acquireWorkSlot(ctx, next); err != nil {
		t.Fatalf("old blocked slot was not reclaimed: %v", err)
	}
	assertContinuationSlots(t, c, cmd.ID, 0)
}

func TestWaitingTransitionCannotReleaseSubmittedOrStaleLeaseSlots(t *testing.T) {
	for _, change := range []string{
		`UPDATE runtime_commands SET submission_started=true WHERE id=$1`,
		`UPDATE runtime_commands SET lease_until=now()-interval '1 second' WHERE id=$1`,
	} {
		c, req, _ := continuationFixture(t)
		ctx := context.Background()
		cmd := acceptedContinuation(t, c, req, "must keep admission")
		if err := c.acquireWorkSlot(ctx, cmd); err != nil {
			t.Fatal(err)
		}
		if _, err := c.ledger.db.Exec(change, cmd.ID); err != nil {
			t.Fatal(err)
		}
		if err := c.waitContinuation(ctx, cmd, "preparing", "", true, nil); !errors.Is(err, errLeaseLost) {
			t.Fatalf("stale/previously submitted command transitioned: %v", err)
		}
		assertContinuationSlots(t, c, cmd.ID, 1)
	}
}
