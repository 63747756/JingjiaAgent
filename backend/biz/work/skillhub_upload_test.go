package work

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/pkg/secretbox"
	"github.com/google/uuid"
)

func hubUploadFixture(t *testing.T, handler http.HandlerFunc) (*Service, *domain.User, HubUploadRequest) {
	t.Helper()
	dsn := os.Getenv("WORK_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("WORK_POSTGRES_TEST_DSN is not set")
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "hub_upload_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec("DROP SCHEMA " + schema + " CASCADE"); admin.Close() })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	repository, err := openRepository(u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repository.Shutdown() })
	conn := repository.db
	ddl := `CREATE TABLE agent_skills(id uuid PRIMARY KEY,name text,description text,scope_type text,scope_id text,publication_state text,enabled bool,active_version_id uuid,is_deleted bool DEFAULT false);
CREATE TABLE agent_skill_versions(id uuid PRIMARY KEY,resource_id uuid,version text,content_hash text DEFAULT '',created_at timestamptz DEFAULT now(),s3_key text,parsed_meta jsonb DEFAULT '{}');
CREATE TABLE work_skillhub_bindings(user_id uuid PRIMARY KEY,registry_url text,token_ciphertext text);
CREATE TABLE work_skillhub_uploads(id uuid PRIMARY KEY,user_id uuid,skill_id uuid,version_id uuid,registry_url text,namespace text,slug text,remote_version text,package_hash text,status text,result jsonb DEFAULT '{}',updated_at timestamptz DEFAULT now(),UNIQUE(user_id,skill_id,registry_url,namespace,remote_version));`
	if _, err := conn.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/cli/v1/skills/global/publish/validate" {
			io.WriteString(w, `{"code":0,"data":{"valid":true,"resolvedSlug":"fixture","errors":[],"warnings":[]}}`)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.NewFromFile(key)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := box.Seal("fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	uid, sid, vid := uuid.New(), uuid.New(), uuid.New()
	if _, err := conn.Exec(`INSERT INTO work_skillhub_bindings VALUES($1,$2,$3)`, uid, server.URL, cipher); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO agent_skills(id,name,description,scope_type,scope_id,publication_state,enabled,active_version_id) VALUES($1,'fixture','test','user',$2,'draft',true,$3)`, sid, uid.String(), vid); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO agent_skill_versions(id,resource_id,version,s3_key) VALUES($1,$2,'v1','fixture.zip')`, vid, sid); err != nil {
		t.Fatal(err)
	}
	data := zipFixture(t, map[string]string{"SKILL.md": "---\nname: fixture\ndescription: test\n---\nfixture"})
	svc := &Service{
		cfg: &config.Config{Work: config.Work{Enabled: true, SkillHub: config.SkillHub{
			URL: server.URL, AllowHTTP: true, SecretKeyFile: key,
		}}},
		store: repository, objects: &memorySkills{data: map[string][]byte{"fixture.zip": data}},
	}
	return svc, &domain.User{ID: uid}, HubUploadRequest{ID: sid, Version: "1.0.0"}
}

func TestHubUploadRetriesDefiniteFailuresAndDeduplicatesSuccess(t *testing.T) {
	for _, status := range []int{400, 401, 403, 422, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var publishes atomic.Int32
			svc, user, req := hubUploadFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/skills/global/publish" {
					t.Errorf("definite rejection must not require remote reconciliation: %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				if publishes.Add(1) == 1 {
					w.WriteHeader(status)
					io.WriteString(w, `{"code":400,"msg":"request rejected"}`)
					return
				}
				io.WriteString(w, `{"code":0,"data":{"status":"PENDING_REVIEW","slug":"fixture","version":"1.0.0"}}`)
			})
			ctx := context.Background()
			_, firstErr := svc.UploadHubSkill(ctx, user, req)
			if firstErr == nil {
				t.Fatal("rejected upload unexpectedly succeeded")
			}
			if status == 401 {
				var input *skillValidationError
				if !errors.As(firstErr, &input) || !input.binding {
					t.Fatalf("binding error lost through HTTP status wrapper: %v", firstErr)
				}
			}
			var state string
			if err := svc.store.db.QueryRow(`SELECT status FROM work_skillhub_uploads`).Scan(&state); err != nil || state != "failed" {
				t.Fatalf("rejection not marked retryable: %s %v", state, err)
			}
			if status == 422 {
				svc.objects.(*memorySkills).data["fixture.zip"] = zipFixture(t, map[string]string{
					"SKILL.md": "---\nname: fixture\ndescription: corrected\n---\nfixed",
				})
			}
			for i := 0; i < 2; i++ {
				out, err := svc.UploadHubSkill(ctx, user, req)
				if err != nil || out.Status != "PENDING_REVIEW" {
					t.Fatalf("retry/replay failed: %+v %v", out, err)
				}
			}
			var records int
			if err := svc.store.db.QueryRow(`SELECT count(*) FROM work_skillhub_uploads`).Scan(&records); err != nil {
				t.Fatal(err)
			}
			if publishes.Load() != 2 || records != 1 {
				t.Fatalf("duplicate upload: calls=%d records=%d", publishes.Load(), records)
			}
		})
	}
}

func TestHubUploadUncertainResultsReconcileWithoutRepublishing(t *testing.T) {
	for _, response := range []string{"server failure", "lost connection", "malformed success"} {
		t.Run(response, func(t *testing.T) {
			var publishes atomic.Int32
			var visible atomic.Bool
			svc, user, req := hubUploadFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/skills/global/publish" {
					publishes.Add(1)
					switch response {
					case "server failure":
						w.WriteHeader(503)
						io.WriteString(w, `{"code":503,"msg":"unavailable"}`)
					case "lost connection":
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						conn.Close()
					case "malformed success":
						io.WriteString(w, `{"code":0`)
					}
					return
				}
				if visible.Load() {
					io.WriteString(w, `{"code":0,"data":{"status":"PENDING_REVIEW"}}`)
				} else {
					w.WriteHeader(404)
					io.WriteString(w, `{"code":404,"msg":"version does not exist"}`)
				}
			})
			ctx := context.Background()
			if _, err := svc.UploadHubSkill(ctx, user, req); err == nil {
				t.Fatal("uncertain upload unexpectedly succeeded")
			}
			var state string
			if err := svc.store.db.QueryRow(`SELECT status FROM work_skillhub_uploads`).Scan(&state); err != nil || state != "unknown" {
				t.Fatalf("uncertain result became retryable: %s %v", state, err)
			}
			if _, err := svc.UploadHubSkill(ctx, user, req); err == nil || !strings.Contains(err.Error(), "尚未确认") {
				t.Fatalf("uncertain upload was retried: %v", err)
			}
			visible.Store(true)
			out, err := svc.UploadHubSkill(ctx, user, req)
			if err != nil || out.Status != "PENDING_REVIEW" || publishes.Load() != 1 {
				t.Fatalf("reconciliation duplicated publication: %+v %v calls=%d", out, err, publishes.Load())
			}
		})
	}
}
