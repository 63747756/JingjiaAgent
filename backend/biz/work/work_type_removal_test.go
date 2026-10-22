package work

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestWorkTypeRemovalSchemaAndPresetPersistence(t *testing.T) {
	dsn := os.Getenv("WORK_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("WORK_POSTGRES_TEST_DSN is not set")
	}
	for _, oldDevelopmentSchema := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh schema", true: "development schema cleanup"}[oldDevelopmentSchema], func(t *testing.T) {
			conn, err := sql.Open("postgres", dsn)
			if err != nil {
				t.Fatal(err)
			}
			conn.SetMaxOpenConns(1)
			defer conn.Close()
			schema := `"work_types_` + uuid.NewString() + `"`
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := conn.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec("CREATE SCHEMA " + schema)
			defer conn.Exec("DROP SCHEMA " + schema + " CASCADE")
			exec("SET search_path TO " + schema)
			exec(`CREATE TABLE users(id uuid PRIMARY KEY); CREATE TABLE teams(id uuid PRIMARY KEY);
CREATE TABLE tasks(id uuid PRIMARY KEY); CREATE TABLE projects(id uuid PRIMARY KEY,deleted_at timestamptz);`)
			apply := func(name string) {
				t.Helper()
				data, err := os.ReadFile(filepath.Join("..", "..", "migration", name))
				if err != nil {
					t.Fatal(err)
				}
				exec(string(data))
			}
			apply("000035_work_projects.up.sql")
			uid, team, profile, task := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			exec(`INSERT INTO users VALUES($1)`, uid)
			exec(`INSERT INTO teams VALUES($1)`, team)
			exec(`INSERT INTO tasks VALUES($1)`, task)
			exec(`INSERT INTO work_execution_profiles(id,team_id,name,model_id,host_id,image_id,created_by) VALUES($1,$2,'助手',$3,'node',$4,$5)`, profile, team, uuid.New(), uuid.New(), uid)
			apply("000038_assistant_tasks.up.sql")
			exec(`INSERT INTO work_tasks(task_id,owner_id,team_id,profile_id,task_preset) SELECT $1,$2,$3,$4,task_presets->1 FROM work_execution_profiles WHERE id=$4`, task, uid, team, profile)
			if oldDevelopmentSchema {
				exec(`ALTER TABLE work_execution_profiles ADD COLUMN scenarios jsonb;
ALTER TABLE work_tasks ADD COLUMN scenario text CHECK(scenario IN ('general','code'));
UPDATE work_execution_profiles SET task_presets=jsonb_build_array(jsonb_build_object('id','custom','name','自定义任务','description','保留业务要求','scenario','code','enabled',true));
UPDATE work_tasks SET task_preset=jsonb_build_object('id','custom','name','自定义任务','description','保留业务要求','scenario','code','enabled',true);`)
			}
			apply("000041_remove_work_types.up.sql")
			apply("000045_assistant_skill_mentions.up.sql")
			var removed bool
			if err := conn.QueryRow(`SELECT NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name IN ('work_tasks','work_execution_profiles') AND column_name IN ('scenario','scenarios'))`).Scan(&removed); err != nil || !removed {
				t.Fatalf("work type columns remain: %v", err)
			}
			p, err := scanProfile(conn.QueryRow(`SELECT `+profileColumns+` FROM work_execution_profiles WHERE id=$1`, profile))
			if err != nil {
				t.Fatal(err)
			}
			if err := validateTaskPresets(&p); err != nil {
				t.Fatal(err)
			}
			if !oldDevelopmentSchema && (len(p.TaskPresets) != 4 || p.TaskPresets[1].Icon != "chart-column") {
				t.Fatalf("fresh presets have incorrect icons: %#v", p.TaskPresets)
			}
			metadata, err := (&Repository{db: conn}).metadata(context.Background(), task)
			if err != nil || metadata.TaskPreset == nil || metadata.TaskPreset.Icon == "" {
				t.Fatalf("metadata cannot load after column removal: %#v %v", metadata, err)
			}
			var raw string
			if err := conn.QueryRow(`SELECT task_presets::text FROM work_execution_profiles WHERE id=$1`, profile).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var definitions []map[string]any
			if err := json.Unmarshal([]byte(raw), &definitions); err != nil {
				t.Fatal(err)
			}
			for _, definition := range definitions {
				if _, ok := definition["scenario"]; ok {
					t.Fatal("obsolete task type remains in stored JSON")
				}
			}
		})
	}
}
