package work

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func postgresRepository(t *testing.T) *Repository {
	t.Helper()
	dsn := os.Getenv("WORK_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("WORK_POSTGRES_TEST_DSN is not set")
	}
	r, err := openRepository(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Shutdown() })
	return r
}

func TestAdmissionLocksLeaveBusinessConnectionsAvailable(t *testing.T) {
	r := postgresRepository(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// All admission requests have acquired their locks before any of them
	// reaches its business queries. The shared-pool implementation deadlocks.
	for i := 0; i < 8; i++ {
		unlock, err := r.requestLock(ctx, uuid.New(), uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
	}
	var n int
	if err := r.db.QueryRowContext(ctx, "SELECT 1").Scan(&n); err != nil || n != 1 {
		t.Fatalf("admission exhausted business connections: n=%d err=%v", n, err)
	}
	// Waiting for a lock connection also observes caller cancellation.
	short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	if unlock, err := r.requestLock(short, uuid.New(), uuid.New()); err == nil {
		unlock()
		t.Fatal("full lock pool ignored cancellation")
	}
}

func TestRequestLocksSerializeAcrossRepositoriesAndReleaseAfterCancellation(t *testing.T) {
	a, b := postgresRepository(t), postgresRepository(t)
	uid, rid := uuid.New(), uuid.New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	unlock, err := a.requestLock(ctx, uid, rid)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	wait, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	if release, err := b.requestLock(wait, uid, rid); err == nil {
		release()
		t.Fatal("another repository acquired the same request lock")
	}
	unlock()
	unlock() // Cleanup must not unlock a later request that reused the session.
	release, err := b.requestLock(ctx, uid, rid)
	if err != nil {
		t.Fatalf("canceled waiter leaked a session lock: %v", err)
	}
	release()
}
