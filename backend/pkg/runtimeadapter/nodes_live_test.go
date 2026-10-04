package runtimeadapter

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	hostrepo "github.com/chaitin/MonkeyCode/backend/biz/host/repo"
	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/consts"
	"github.com/chaitin/MonkeyCode/backend/db"
	"github.com/chaitin/MonkeyCode/backend/db/enttest"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/redis/go-redis/v9"
	"github.com/samber/do"
)

// Real authenticated Docker node metadata + production registration repository.
// Business tables here are isolated SQLite, the binding ledger is PostgreSQL.
// This does not represent original Web installation or production capacity.
func TestLiveRuntimeNodeEnrollment(t *testing.T) {
	if os.Getenv("RUNTIME_NODE_LIVE_TEST") != "1" {
		t.Skip("requires real daemon and isolated PostgreSQL")
	}
	ctx := context.Background()
	l := testLedger(t)
	business := enttest.Open(t, "sqlite3", "file:live-node-enrollment?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = business.Close() })
	owner, teamID := uuid.New(), uuid.New()
	business.User.Create().SetID(owner).SetName("isolated administrator").SetRole(consts.UserRoleEnterprise).SetStatus(consts.UserStatusActive).SaveX(ctx)
	business.Team.Create().SetID(teamID).SetName("isolated runtime node team").SetMemberLimit(5).SaveX(ctx)
	business.TeamMember.Create().SetID(uuid.New()).SetTeamID(teamID).SetUserID(owner).SetRole(consts.TeamMemberRoleAdmin).SaveX(ctx)
	i := do.New()
	do.ProvideValue[*db.Client](i, business)
	do.ProvideValue(i, &config.Config{})
	do.ProvideValue(i, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = rdb.Close() })
	do.ProvideValue(i, rdb)
	repo, err := hostrepo.NewHostRepo(i)
	if err != nil {
		t.Fatal(err)
	}
	node := config.RuntimeNode{ID: uuid.NewString(), OwnerID: owner.String(), TeamID: teamID.String(), URL: os.Getenv("RUNTIME_TEST_URL"), TokenFile: os.Getenv("RUNTIME_TEST_TOKEN_FILE")}
	e, err := NewEngine(node)
	if err != nil {
		t.Fatal("cannot configure private live node")
	}
	actual, err := e.nodeSnapshot(ctx)
	if err != nil {
		t.Fatal("real Docker node metadata was not available")
	}
	c := &Client{ledger: l, registry: repo.(HostRegistry), backend: "agent_compose", nodes: map[string]config.RuntimeNode{node.ID: node}, engines: map[string]*Engine{node.ID: e}}
	if err := c.SyncNodes(ctx); err != nil {
		t.Fatal(err)
	}
	listed, err := c.Host().List(ctx, owner.String())
	if err != nil || len(listed) != 1 {
		t.Fatal("real node enrollment did not appear in authorized host listing")
	}
	host := listed[node.ID]
	if host.Cores != actual.Cores || host.Memory != actual.Memory || host.Arch != actual.Arch || host.Hostname != actual.Hostname || host.Version != actual.Version {
		t.Fatal("registered node did not use actual Docker capacity")
	}
	if online, err := c.Host().IsOnline(ctx, &taskflow.IsOnlineReq[string]{IDs: []string{node.ID}}); err != nil || !online.OnlineMap[node.ID] {
		t.Fatal("registered real node was not online")
	}
	if stranger, err := c.Host().List(ctx, uuid.NewString()); err != nil || len(stranger) != 0 {
		t.Fatal("configured live node became visible without original grants")
	}
	restarted := &Client{ledger: l, registry: repo.(HostRegistry), nodes: c.nodes, engines: c.engines}
	if err := restarted.SyncNodes(ctx); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := l.db.QueryRowContext(ctx, `SELECT instance_id FROM runtime_nodes WHERE node_id=$1`, node.ID).Scan(&stored); err != nil || stored != actual.InstanceID {
		t.Fatal("recreated runtime client lost node identity")
	}
	t.Logf("Real Docker node enrolled: cores=%d memory_bytes=%d; authorized listing and persistent PostgreSQL identity passed", actual.Cores, actual.Memory)
}
