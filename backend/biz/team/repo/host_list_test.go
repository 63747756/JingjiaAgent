package repo

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/63747756/jingjiaagent/backend/db"
)

func TestTeamHostListExcludesSoftDeletedHostRelations(t *testing.T) {
	ctx := context.Background()
	client := newTeamRepoTestDB(t)
	teamID := createTeamRepoTestTeam(t, client)
	otherTeamID := createTeamRepoTestTeam(t, client)
	ownerID := createTeamRepoNamedUser(t, client, "host-list@example.test")
	create := func(teamID uuid.UUID) *db.Host {
		t.Helper()
		h, err := client.Host.Create().SetID(uuid.NewString()).SetUserID(ownerID).Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.TeamHost.Create().SetID(uuid.New()).SetTeamID(teamID).SetHostID(h.ID).Save(ctx); err != nil {
			t.Fatal(err)
		}
		return h
	}
	active := create(teamID)
	removed := create(teamID)
	create(otherTeamID)
	// Dynamic unbinding preserves the team relation for a soft-deleted host.
	if err := client.Host.UpdateOneID(removed.ID).SetDeletedAt(time.Now()).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	repo := &TeamHostRepo{db: client}
	hosts, err := repo.List(ctx, teamID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0] == nil || hosts[0].ID != active.ID {
		t.Fatalf("list must contain only the current team's live host, got %#v", hosts)
	}
	if err := client.Host.UpdateOneID(active.ID).SetDeletedAt(time.Now()).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	hosts, err = repo.List(ctx, teamID)
	if err != nil || len(hosts) != 0 {
		t.Fatalf("all hosts unbound: hosts=%#v, err=%v", hosts, err)
	}
}
