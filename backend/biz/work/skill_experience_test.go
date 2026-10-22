package work

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/db/agentskill"
	"github.com/63747756/jingjiaagent/backend/db/agentskillrepo"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func zipFixture(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, data := range files {
		f, e := w.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		f.Write([]byte(data))
	}
	w.Close()
	return b.Bytes()
}
func TestPackageEditingRetainsScriptsAndNormalizesSingleFolder(t *testing.T) {
	data := zipFixture(t, map[string]string{"example/SKILL.md": "---\nname: example\ndescription: test\ntags: [data]\n---\nOriginal", "example/scripts/run.py": "print('retained')", "example/templates/report.md": "retained template"})
	normalized, meta, err := normalizeSkillZip(data)
	if err != nil || meta.Name != "example" || len(meta.Tags) != 1 {
		t.Fatalf("normalize: %#v %v", meta, err)
	}
	updated, err := replaceSkillMarkdown(normalized, "---\nname: example\ndescription: edited\n---\nEdited")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := zip.NewReader(bytes.NewReader(updated), int64(len(updated)))
	seen := map[string]string{}
	for _, f := range r.File {
		body, _ := f.Open()
		b, _ := io.ReadAll(body)
		body.Close()
		seen[f.Name] = string(b)
	}
	if len(seen) != 3 || seen["scripts/run.py"] != "print('retained')" || seen["templates/report.md"] != "retained template" || !strings.Contains(seen["SKILL.md"], "Edited") {
		t.Fatalf("lost package files: %v", seen)
	}
}
func TestPackagesRejectMultipleRootsAndUnsafePaths(t *testing.T) {
	for _, files := range []map[string]string{{"../SKILL.md": "bad"}, {"SKILL.md": "---\nname: one\ndescription: test\n---", "nested/SKILL.md": "other"}, {"folder/SKILL.md": "---\nname: one\ndescription: test\n---", "outside.py": "escape"}} {
		if _, _, err := normalizeSkillZip(zipFixture(t, files)); err == nil {
			t.Fatal("invalid package accepted")
		}
	}
}
func TestExplicitSkillsDoNotLoseToUnselectedSameName(t *testing.T) {
	s, personal, uid, team := skillFixture(t)
	ctx := context.Background()
	repo := s.ent.AgentSkillRepo.Create().SetName("global").SetSourceType(agentskillrepo.SourceTypeBare).SetScopeType(agentskillrepo.ScopeTypeGlobal).SetScopeID("global").SetCreatedBy(uid).SaveX(ctx)
	shared := s.ent.AgentSkill.Create().SetRepoID(repo.ID).SetName(personal.Name).SetScopeType(agentskill.ScopeTypeGlobal).SetCreatedBy(uid).SaveX(ctx)
	v := s.ent.AgentSkillVersion.Create().SetResourceID(shared.ID).SetVersion("v1").SetS3Key("old.zip").SaveX(ctx)
	s.ent.AgentSkill.UpdateOneID(shared.ID).SetActiveVersionID(v.ID).SaveX(ctx)
	s.ent.AgentSkill.UpdateOneID(personal.ID).SetEnabled(false).SaveX(ctx)
	pins, _, err := s.pinSkills(ctx, uid, team, []string{shared.ID.String()}, nil)
	if err != nil || len(pins) != 1 || pins[0].SkillID != shared.ID {
		t.Fatalf("disabled unselected skill shadowed explicit choice: %v", err)
	}
	s.ent.AgentSkill.UpdateOneID(personal.ID).SetEnabled(true).SaveX(ctx)
	if _, _, err = s.pinSkills(ctx, uid, team, []string{shared.ID.String(), personal.ID.String()}, nil); !errors.Is(err, errcode.ErrBadRequest) {
		t.Fatalf("name conflict was silently overridden: %v", err)
	}
}
func TestMarketRequestsNeverCarryCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("market leaked credentials")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": []any{}, "total": 0}})
	}))
	defer server.Close()
	s := &Service{cfg: &config.Config{Work: config.Work{Enabled: true, SkillHub: config.SkillHub{URL: server.URL, AllowHTTP: true}}}}
	if _, err := s.HubMarket(context.Background(), "test", 0, 20); err != nil {
		t.Fatal(err)
	}
}
func TestHubSessionRefreshesCSRFAfterLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/methods":
			http.SetCookie(w, &http.Cookie{Name: "XSRF-TOKEN", Value: "before", Path: "/"})
		case "/api/v1/auth/local/login":
			if r.Header.Get("X-XSRF-TOKEN") != "before" {
				t.Error("initial CSRF missing")
			}
			http.SetCookie(w, &http.Cookie{Name: "XSRF-TOKEN", Value: "after", Path: "/"})
			http.SetCookie(w, &http.Cookie{Name: "SESSION", Value: "temporary", Path: "/"})
		case "/api/v1/tokens":
			if r.Header.Get("X-XSRF-TOKEN") != "after" {
				t.Error("stale CSRF after login")
			}
			if _, err := r.Cookie("SESSION"); err != nil {
				t.Error("temporary session missing")
			}
		}
		io.WriteString(w, `{"code":0,"data":{}}`)
	}))
	defer server.Close()
	h, err := newHubSession(config.SkillHub{URL: server.URL, AllowHTTP: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/api/v1/auth/methods", "/api/v1/auth/local/login", "/api/v1/tokens"} {
		method := http.MethodPost
		if strings.HasSuffix(p, "methods") {
			method = http.MethodGet
		}
		if err = h.json(context.Background(), method, p, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
}
func TestHubRejectsUnexpectedDownloadRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://unexpected.invalid/package.zip", 302)
	}))
	defer server.Close()
	h, _ := newHubSession(config.SkillHub{URL: server.URL, AllowHTTP: true}, false)
	if _, err := h.download(context.Background(), "/download"); !errors.Is(err, errcode.ErrForbidden) {
		t.Fatalf("unapproved storage host accepted: %v", err)
	}
}
