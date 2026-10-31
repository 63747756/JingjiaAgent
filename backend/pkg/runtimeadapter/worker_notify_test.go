package runtimeadapter

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func expectWorkerNotification(t *testing.T, c *Client, want bool) {
	t.Helper()
	select {
	case <-c.workerWake:
		if !want {
			t.Fatal("unexpected worker notification")
		}
	default:
		if want {
			t.Fatal("missing worker notification")
		}
	}
}

func TestWorkerNotificationCoalescesAndNeverBlocks(t *testing.T) {
	c := &Client{workerWake: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			c.notifyWorker()
		}
		// Older test fixtures and clients without a worker may have no channel.
		(&Client{}).notifyWorker()
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker notification blocked its caller")
	}
	expectWorkerNotification(t, c, true)
	expectWorkerNotification(t, c, false)
}

func TestWorkerNotificationRequiresSuccessfulCommit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "committed", true: "commit failed"}[fail], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			c := &Client{workerWake: make(chan struct{}, 1)}
			mock.ExpectBegin()
			commit := mock.ExpectCommit()
			failure := errors.New("commit failed")
			if fail {
				commit.WillReturnError(failure)
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			expectWorkerNotification(t, c, false)
			err = c.commitAndNotifyWorker(tx)
			if fail && !errors.Is(err, failure) || !fail && err != nil {
				t.Fatalf("unexpected commit result: %v", err)
			}
			expectWorkerNotification(t, c, !fail)
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkerNotificationWakesNormalClaimBeforePollingFallback(t *testing.T) {
	claims := make(chan struct{}, 4)
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(expected, actual string) error {
		if err := sqlmock.QueryMatcherRegexp.Match(expected, actual); err != nil {
			return err
		}
		claims <- struct{}{}
		return nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for range 2 {
		mock.ExpectQuery(regexp.QuoteMeta("UPDATE runtime_commands SET lease_token=")).WillReturnError(sql.ErrNoRows)
	}
	c := &Client{ledger: &Ledger{db: db}, poll: time.Hour, workerWake: make(chan struct{}, 1), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.RunWorker(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("worker shutdown: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	}()
	awaitClaim := func() {
		t.Helper()
		select {
		case <-claims:
		case <-time.After(time.Second):
			t.Fatal("worker waited for its polling fallback")
		}
	}
	awaitClaim()
	c.notifyWorker()
	awaitClaim()
}

func TestContinuationNotificationsPreserveClaimOrderingAndLeases(t *testing.T) {
	c, req, _ := continuationFixture(t)
	c.workerWake = make(chan struct{}, 1)
	cmd := acceptedContinuation(t, c, req, "prepare this round")
	expectWorkerNotification(t, c, true)
	var profile uuid.UUID
	if err := c.ledger.db.QueryRow(`SELECT profile_id FROM work_tasks WHERE task_id=$1`, req.ID).Scan(&profile); err != nil {
		t.Fatal(err)
	}
	other := workRuntimeTask(t, c.ledger, profile)
	ctx := context.Background()
	if err := c.prepareContinuation(ctx, &cmd); err != nil {
		t.Fatal(err)
	}
	expectWorkerNotification(t, c, true)
	if state, phase := continuationState(t, c, cmd.ID); state != "pending" || phase != "starting" {
		t.Fatalf("notification preceded durable preparation: %s/%s", state, phase)
	}
	// A notification has no command ID: another older eligible command wins.
	older, err := c.ledger.Claim(ctx)
	if err != nil || older.TaskID != other.ID.String() {
		t.Fatalf("notification bypassed queue order: %+v %v", older, err)
	}
	ready, err := c.ledger.Claim(ctx)
	if err != nil || ready.ID != cmd.ID || ready.Lease == cmd.Lease || ready.State != "pending" {
		t.Fatalf("continuation did not acquire a fresh lease: %+v %v", ready, err)
	}
	// Stale and duplicate wakeups cannot bypass the database lease.
	for range 10 {
		c.notifyWorker()
	}
	if _, err := c.ledger.Claim(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("duplicate notification reclaimed leased work: %v", err)
	}
}

func TestContinuationNotificationsPreserveCancellationAndBackoff(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "saving backoff", true: "canceled"}[canceled], func(t *testing.T) {
			c, req, _ := continuationFixture(t)
			c.workerWake = make(chan struct{}, 1)
			cmd := acceptedContinuation(t, c, req, "do not execute yet")
			expectWorkerNotification(t, c, true)
			if canceled {
				if _, err := c.ledger.db.Exec(`UPDATE runtime_commands SET cancel_requested=true WHERE id=$1`, cmd.ID); err != nil {
					t.Fatal(err)
				}
			} else if _, err := c.ledger.db.Exec(`UPDATE runtime_environments SET state='checkpointing' WHERE id=$1`, req.VMID); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if err := c.prepareContinuation(ctx, &cmd); err != nil {
				t.Fatal(err)
			}
			expectWorkerNotification(t, c, false)
			state, phase := continuationState(t, c, cmd.ID)
			if canceled && state != "canceled" || !canceled && (state != "waiting" || phase != "saving") {
				t.Fatalf("unexpected preparation result: %s/%s", state, phase)
			}
			c.notifyWorker()
			if _, err := c.ledger.Claim(ctx); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("notification bypassed cancellation or retry deadline: %v", err)
			}
		})
	}
}

func TestRejectedContinuationDoesNotNotifyWorker(t *testing.T) {
	c, req, _ := continuationFixture(t)
	c.workerWake = make(chan struct{}, 1)
	if _, err := c.ledger.db.Exec(`UPDATE runtime_task_intents SET deletion_requested=true WHERE task_id=$1`, req.ID); err != nil {
		t.Fatal(err)
	}
	err := c.acceptContinuation(context.Background(), &taskflow.Task{ID: req.ID, Text: "rejected", ClientMessageID: uuid.NewString()})
	if err == nil || !strings.Contains(err.Error(), "删除") {
		t.Fatalf("continuation should be rejected: %v", err)
	}
	expectWorkerNotification(t, c, false)
}
