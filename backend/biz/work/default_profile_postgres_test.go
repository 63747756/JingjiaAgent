package work

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/google/uuid"
)

func TestDefaultProfilePostgresSwitchWithImmediateUniqueIndex(t *testing.T) {
	dsn := os.Getenv("WORK_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("WORK_POSTGRES_TEST_DSN is not set")
	}
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Connection-local tables leave the acceptance application's data untouched.
	exec(`CREATE TEMP TABLE teams(id uuid,deleted_at timestamptz);
CREATE TEMP TABLE team_members(team_id uuid,user_id uuid,role text);
CREATE TEMP TABLE work_execution_profiles(id uuid,team_id uuid,enabled boolean,is_default boolean NOT NULL,updated_at timestamptz);
CREATE UNIQUE INDEX default_profile_temp_unique ON work_execution_profiles(team_id) WHERE is_default AND enabled;`)
	uid, team, first, second := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO teams VALUES($1,NULL)`, team)
	exec(`INSERT INTO team_members VALUES($1,$2,'admin')`, team, uid)
	exec(`INSERT INTO work_execution_profiles VALUES($1,$3,true,true,now()),($2,$3,true,false,now())`, first, second, team)
	cfg := &config.Config{}
	cfg.Work.Enabled = true
	s := &Service{cfg: cfg, store: &Repository{db: conn}}
	for _, selected := range []*uuid.UUID{&second, &first, &second, &second, nil} {
		if err := s.SetDefaultProfile(ctx, &domain.User{ID: uid}, DefaultProfileRequest{TeamID: team, ProfileID: selected}); err != nil {
			t.Fatalf("switch with unique index: %v", err)
		}
		var count int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM work_execution_profiles WHERE team_id=$1 AND is_default`, team).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if (selected == nil && count != 0) || (selected != nil && count != 1) {
			t.Fatalf("invalid default count %d", count)
		}
		if selected != nil {
			var actual uuid.UUID
			if err := conn.QueryRowContext(ctx, `SELECT id FROM work_execution_profiles WHERE team_id=$1 AND is_default`, team).Scan(&actual); err != nil || actual != *selected {
				t.Fatalf("wrong default: %v %v", actual, err)
			}
		}
	}
}
