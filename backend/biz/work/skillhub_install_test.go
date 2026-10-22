package work

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/google/uuid"
)

func hubInstallFixture(t *testing.T, version *string) (*Service, *domain.User) {
	t.Helper()
	s, user, _ := hubUploadFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("installation must remain anonymous")
		}
		switch r.URL.Path {
		case "/api/v1/skills/global/installed-skill":
			io.WriteString(w, `{"code":0,"data":{"visibility":"PUBLIC"}}`)
		case "/api/cli/v1/skills/global/installed-skill/resolve":
			fmt.Fprintf(w, `{"code":0,"data":{"version":%q,"versionId":1}}`, *version)
		case "/api/cli/v1/skills/global/installed-skill/versions/" + *version + "/download":
			w.Header().Set("Content-Type", "application/zip")
			w.Write(hubInstallPackage(t, *version))
		default:
			http.NotFound(w, r)
		}
	})
	_, err := s.store.db.Exec(`
CREATE TABLE users(id uuid PRIMARY KEY,deleted_at timestamptz);
CREATE TABLE agent_skill_repos(id uuid PRIMARY KEY,name text,scope_type text,scope_id text,created_by uuid,source_type text,is_deleted bool DEFAULT false,created_at timestamptz DEFAULT now());
ALTER TABLE agent_skills ADD COLUMN repo_id uuid, ADD COLUMN created_by uuid, ADD COLUMN updated_at timestamptz DEFAULT now();
CREATE TABLE work_skillhub_installs(user_id uuid,registry_url text,namespace text,slug text,skill_id uuid,remote_version text,package_hash text,updated_at timestamptz DEFAULT now(),PRIMARY KEY(user_id,registry_url,namespace,slug));
`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.db.Exec(`INSERT INTO users(id) VALUES($1)`, user.ID); err != nil {
		t.Fatal(err)
	}
	return s, user
}

func hubInstallPackage(t *testing.T, version string) []byte {
	t.Helper()
	// Use one entry so the raw source hash is deterministic across requests.
	return zipFixture(t, map[string]string{"SKILL.md": "---\nname: installed-skill\ndescription: installation fixture\n---\n" + version})
}

func TestHubInstallFailureRollsBackAndCanRetry(t *testing.T) {
	for _, updating := range []bool{false, true} {
		t.Run(fmt.Sprintf("updating=%t", updating), func(t *testing.T) {
			version := "1.0.0"
			s, user := hubInstallFixture(t, &version)
			ctx := context.Background()
			req := HubInstallRequest{Namespace: "global", Slug: "installed-skill", Version: version}
			var previous SkillView
			if updating {
				var err error
				previous, err = s.InstallHubSkill(ctx, user, req)
				if err != nil {
					t.Fatal(err)
				}
				disabled := false
				if err = s.SetPersonalSkillEnabled(ctx, user, previous.ID, &disabled); err != nil {
					t.Fatal(err)
				}
				version, req.Version = "2.0.0", "2.0.0"
			}
			if _, err := s.store.db.Exec(`
CREATE FUNCTION reject_install() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'simulated source write failure'; END; $$;
CREATE TRIGGER reject_install BEFORE INSERT ON work_skillhub_installs FOR EACH ROW EXECUTE FUNCTION reject_install();
`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.InstallHubSkill(ctx, user, req); err == nil || !strings.Contains(err.Error(), "simulated source write failure") {
				t.Fatalf("did not reach injected failure: %v", err)
			}
			if updating {
				got, err := s.Skill(ctx, user, previous.ID)
				if err != nil || got.Enabled || *got.ActiveVersionID != *previous.ActiveVersionID || len(got.Versions) != 1 || got.SourceVersion != "1.0.0" {
					t.Fatalf("failed update changed existing skill: %+v %v", got, err)
				}
				var installedVersion string
				if err = s.store.db.QueryRow(`SELECT remote_version FROM work_skillhub_installs WHERE skill_id=$1`, previous.ID).Scan(&installedVersion); err != nil || installedVersion != "1.0.0" {
					t.Fatalf("source record changed: %q %v", installedVersion, err)
				}
			} else {
				var skills, versions, installs, repos int
				err := s.store.db.QueryRow(`SELECT
 (SELECT count(*) FROM agent_skills WHERE name='installed-skill'),
 (SELECT count(*) FROM agent_skill_versions WHERE parsed_meta->>'source_type'='market'),
 (SELECT count(*) FROM work_skillhub_installs),
 (SELECT count(*) FROM agent_skill_repos)`).Scan(&skills, &versions, &installs, &repos)
				if err != nil || skills != 0 || versions != 0 || installs != 0 || repos != 0 {
					t.Fatalf("failed install left live records: skills=%d versions=%d installs=%d repos=%d err=%v", skills, versions, installs, repos, err)
				}
			}
			if _, err := s.store.db.Exec(`DROP TRIGGER reject_install ON work_skillhub_installs`); err != nil {
				t.Fatal(err)
			}
			out, err := s.InstallHubSkill(ctx, user, req)
			if err != nil {
				t.Fatalf("retry failed: %v", err)
			}
			wantVersions := 1
			if updating {
				wantVersions = 2
				if out.ID != previous.ID {
					t.Fatal("update changed skill identity")
				}
			}
			if out.Enabled == updating || len(out.Versions) != wantVersions || out.SourceType != "market" || out.SourceVersion != version {
				t.Fatalf("retry lost skill state/source: %+v", out)
			}
			var sourceID uuid.UUID
			var sourceVersion, sourceHash string
			err = s.store.db.QueryRow(`SELECT skill_id,remote_version,package_hash FROM work_skillhub_installs`).Scan(&sourceID, &sourceVersion, &sourceHash)
			sum := sha256.Sum256(hubInstallPackage(t, version))
			if err != nil || sourceID != out.ID || sourceVersion != version || sourceHash != hex.EncodeToString(sum[:]) {
				t.Fatalf("source and skill disagree: id=%v version=%q hash=%q err=%v", sourceID, sourceVersion, sourceHash, err)
			}
			replay, err := s.InstallHubSkill(ctx, user, req)
			if err != nil || replay.ID != out.ID || len(replay.Versions) != wantVersions {
				t.Fatalf("repeated installation created another version: %+v %v", replay, err)
			}
		})
	}
}
