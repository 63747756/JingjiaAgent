package runtimeadapter

import (
	"context"
	"errors"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
	"net/http"
	"strings"
	"testing"
)

func TestIdleRecycleRequiresNodeCoverageAndKeepsRejectedWorkspaceUsable(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusNotFound, http.StatusServiceUnavailable, http.StatusNoContent} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c, req, cp, _ := checkpointCompletionFixture(t, "hibernated")
			ctx := context.Background()
			if err := c.CompleteCheckpoint(ctx, req.ID, cp, req.VMID); err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			calls := 0
			mux.HandleFunc("/jingjiaagent/workspace/recycle", func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Query().Get("sandbox_id") != "sandbox-1" || r.URL.Query().Get("checkpoint_id") != cp.String() || len(r.URL.Query().Get("expected_sha256")) != 64 || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("unprotected removal request")
				}
				w.WriteHeader(status)
			})
			c.engines["node"] = testEngine(t, mux)
			err := c.VirtualMachiner().Delete(ctx, &taskflow.DeleteVirtualMachineReq{ID: req.VMID, IdleOnly: true})
			if calls != 1 {
				t.Fatalf("did not use protected endpoint: %v", err)
			}
			var state string
			if e := c.ledger.db.QueryRow("SELECT state FROM runtime_environments WHERE id=$1", req.VMID).Scan(&state); e != nil {
				t.Fatal(e)
			}
			switch status {
			case http.StatusNoContent:
				if err != nil || state != "deleted" {
					t.Fatalf("confirmed removal not committed: %v %s", err, state)
				}
			case http.StatusServiceUnavailable:
				if !errors.Is(err, taskflow.ErrRecycleDeferred) || state != "stopping" {
					t.Fatalf("uncertain removal lost fence: %v %s", err, state)
				}
			default:
				if !errors.Is(err, taskflow.ErrRecycleDeferred) || state != "hibernated" {
					t.Fatalf("declined removal stranded task: %v %s", err, state)
				}
			}
		})
	}
}

func TestFailedArchiveUnfencesUsableWorkspaceAndStopsAutomaticDeleteRetries(t *testing.T) {
	c, req, cp, _ := checkpointCompletionFixture(t, "hibernated")
	ctx := context.Background()
	if _, err := c.ledger.db.Exec(`UPDATE work_storage_jobs SET state='failed',attempts=3 WHERE id=$1`, cp); err != nil {
		t.Fatal(err)
	}
	if err := c.AbortCheckpoint(ctx, req.ID, cp, req.VMID); err != nil {
		t.Fatal(err)
	}
	var usable bool
	if err := c.ledger.db.QueryRow(`SELECT state='hibernated' AND archiving_checkpoint_id IS NULL AND checkpoint_id IS NULL FROM runtime_environments WHERE id=$1`, req.VMID).Scan(&usable); err != nil || !usable {
		t.Fatal("save failure stranded original workspace", err)
	}
	for i := 0; i < 3; i++ {
		c.recordRecycleFailure(ctx, req.VMID, errors.New("node unavailable"))
	}
	if err := c.blockedRecycle(ctx, req.VMID); err == nil {
		t.Fatal("remote failure retries not bounded")
	}
}

func TestFullRestoreRetainsGitIdentityWithoutRecloning(t *testing.T) {
	c, req, cp, _ := checkpointCompletionFixture(t, "hibernated")
	ctx := context.Background()
	if err := c.CompleteCheckpoint(ctx, req.ID, cp, req.VMID); err != nil {
		t.Fatal(err)
	}
	e, err := c.ledger.Environment(ctx, req.VMID)
	if err != nil {
		t.Fatal(err)
	}
	e.Request.Git = taskflow.Git{URL: "https://git.example.test/team/repo.git", Branch: "feature", Username: "fixture"}
	sealed, err := c.ledger.seal(e.ID, mustJSON(e.Request))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ledger.db.Exec(`UPDATE runtime_environments SET payload=$2 WHERE id=$1`, e.ID, sealed); err != nil {
		t.Fatal(err)
	}
	address := "https://objects.example.test/full.zip"
	id, err := c.RestoreCheckpoint(ctx, uuid.MustParse(e.OwnerID), req.ID, cp, address, "archive-key", strings.Repeat("a", 64), nil)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := c.ledger.Environment(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Request.Git != e.Request.Git || restored.Request.ZipUrl != address {
		t.Fatal("full restore lost Git identity or snapshot source")
	}
}

func TestRepositoryRecycleSchedulesFullArchiveInsteadOfPermanentProtection(t *testing.T) {
	c, req, cp, _ := checkpointCompletionFixture(t, "hibernated")
	ctx := context.Background()
	if _, err := c.ledger.db.Exec(`UPDATE workspace_checkpoints SET manifest='{"schema":"jingjiaagent.workspace.checkpoint.v2"}' WHERE id=$1`, cp); err != nil {
		t.Fatal(err)
	}
	if err := c.CompleteCheckpoint(ctx, req.ID, cp, req.VMID); err != nil {
		t.Fatal(err)
	}
	e, err := c.ledger.Environment(ctx, req.VMID)
	if err != nil {
		t.Fatal(err)
	}
	e.Request.Git = taskflow.Git{URL: "https://git.example.test/team/repository.git", Branch: "feature"}
	sealed, err := c.ledger.seal(e.ID, mustJSON(e.Request))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ledger.db.Exec(`UPDATE runtime_environments SET payload=$2 WHERE id=$1`, e.ID, sealed); err != nil {
		t.Fatal(err)
	}
	err = c.VirtualMachiner().Delete(ctx, &taskflow.DeleteVirtualMachineReq{ID: req.VMID, IdleOnly: true})
	if !errors.Is(err, taskflow.ErrRecyclePending) {
		t.Fatal("repository not scheduled for archive", err)
	}
	var full bool
	if err = c.ledger.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM work_storage_jobs WHERE environment_id=$1 AND request->>'scope'='workspace' AND request->>'resume_state'='hibernated')`, req.VMID).Scan(&full); err != nil || !full {
		t.Fatal("missing full archive job", err)
	}
}

func TestAutomaticContinuationRestoresRepositoriesOnlyFromFullCheckpoint(t *testing.T) {
	for _, source := range []string{"git", "zip", "work"} {
		for _, full := range []bool{true, false} {
			name := source + "/partial"
			if full {
				name = source + "/full"
			}
			t.Run(name, func(t *testing.T) {
				c, req, cp, _ := checkpointCompletionFixture(t, "hibernated")
				ctx := context.Background()
				if !full {
					if _, err := c.ledger.db.Exec(`UPDATE workspace_checkpoints SET manifest='{"schema":"jingjiaagent.workspace.checkpoint.v2"}' WHERE id=$1`, cp); err != nil {
						t.Fatal(err)
					}
				}
				if err := c.CompleteCheckpoint(ctx, req.ID, cp, req.VMID); err != nil {
					t.Fatal(err)
				}
				e, err := c.ledger.Environment(ctx, req.VMID)
				if err != nil {
					t.Fatal(err)
				}
				switch source {
				case "git":
					e.Request.Git = taskflow.Git{URL: "https://git.example.test/team/repository.git", Branch: "feature"}
				case "zip":
					e.Request.ZipUrl = "https://objects.example.test/original-project.zip"
				}
				sealed, err := c.ledger.seal(e.ID, mustJSON(e.Request))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = c.ledger.db.Exec(`UPDATE runtime_environments SET payload=$2,state='deleted' WHERE id=$1`, e.ID, sealed); err != nil {
					t.Fatal(err)
				}
				if err = c.ledger.Enqueue(ctx, req.VMID, req.ID.String(), "task", 1, req); err != nil {
					t.Fatal(err)
				}
				if _, err = c.ledger.db.Exec(`UPDATE runtime_commands SET state='complete' WHERE task_id=$1`, req.ID); err != nil {
					t.Fatal(err)
				}
				calls := 0
				var replacement string
				c.SetContinuationRestore(func(ctx context.Context, uid, tid, _ uuid.UUID) (string, error) {
					calls++
					if uid.String() != e.OwnerID || tid != req.ID {
						t.Fatal("wrong restoration task or owner")
					}
					var restoreErr error
					replacement, restoreErr = c.RestoreCheckpoint(ctx, uid, tid, cp, "https://objects.example.test/full.zip", "archive-key", strings.Repeat("a", 64), nil)
					return replacement, restoreErr
				})
				cmd := acceptedContinuation(t, c, req, "continue after policy recycle")
				if err = c.prepareContinuation(ctx, &cmd); err != nil {
					t.Fatal(err)
				}
				state, phase := continuationState(t, c, cmd.ID)
				if calls != 1 {
					t.Fatal("automatic continuation bypassed checkpoint selection", calls, state, phase)
				}
				if !full && source != "work" {
					if state != "blocked" || replacement != "" {
						t.Fatal("partial snapshot replaced repository", state, replacement)
					}
					return
				}
				if state != "waiting" || phase != "restoring" || replacement == "" {
					t.Fatal("saved workspace did not reach automatic restore", state, phase, replacement)
				}
				restored, err := c.ledger.Environment(ctx, replacement)
				if err != nil || restored.Request.Git != e.Request.Git {
					t.Fatal("restoration lost repository identity", err)
				}
			})
		}
	}
}
