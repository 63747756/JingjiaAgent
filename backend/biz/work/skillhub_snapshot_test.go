package work

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type changingHubObjects struct {
	*memorySkills
	gets   int
	change func()
}

func (m *changingHubObjects) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	m.gets++
	out, err := m.memorySkills.GetObject(ctx, key)
	if m.gets == 1 {
		m.change()
	}
	return out, err
}

func TestHubUploadValidatesAndPublishesSameSnapshot(t *testing.T) {
	s, user, req := hubUploadFixture(t, http.NotFound)
	var checked, published []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		data, err := io.ReadAll(file)
		file.Close()
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/cli/v1/skills/global/publish/validate":
			checked = data
			io.WriteString(w, `{"code":0,"data":{"valid":true,"resolvedSlug":"fixture","errors":[],"warnings":[]}}`)
		case "/api/v1/skills/global/publish":
			published = data
			io.WriteString(w, `{"code":0,"data":{"status":"PENDING_REVIEW","slug":"fixture","version":"1.0.0"}}`)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	s.cfg.Work.SkillHub.URL = server.URL
	if _, err := s.store.db.Exec(`UPDATE work_skillhub_bindings SET registry_url=$1`, server.URL); err != nil {
		t.Fatal(err)
	}
	var originalID uuid.UUID
	if err := s.store.db.QueryRow(`SELECT active_version_id FROM agent_skills WHERE id=$1`, req.ID).Scan(&originalID); err != nil {
		t.Fatal(err)
	}
	store := s.objects.(*memorySkills)
	nextID := uuid.New()
	store.data["updated.zip"] = zipFixture(t, map[string]string{"SKILL.md": "---\nname: fixture\ndescription: corrected\n---\nNEW contents"})
	if _, err := s.store.db.Exec(`INSERT INTO agent_skill_versions(id,resource_id,version,s3_key) VALUES($1,$2,'v2','updated.zip')`, nextID, req.ID); err != nil {
		t.Fatal(err)
	}
	objects := &changingHubObjects{memorySkills: store, change: func() {
		if _, err := s.store.db.Exec(`UPDATE agent_skills SET active_version_id=$1 WHERE id=$2`, nextID, req.ID); err != nil {
			t.Fatal(err)
		}
	}}
	s.objects = objects
	out, err := s.UploadHubSkill(context.Background(), user, req)
	if err != nil || out.Status != "PENDING_REVIEW" {
		t.Fatalf("upload failed: %+v %v", out, err)
	}
	if len(checked) == 0 || !bytes.Equal(checked, published) || objects.gets != 1 {
		t.Fatalf("validation/publication diverged or package was reread: checked=%d published=%d reads=%d", len(checked), len(published), objects.gets)
	}
	text, err := skillMarkdown(published)
	if err != nil || strings.Contains(text, "NEW contents") || !strings.Contains(text, "version: 1.0.0") {
		t.Fatalf("published wrong snapshot: %q %v", text, err)
	}
	var recordedID, currentID uuid.UUID
	var hash string
	err = s.store.db.QueryRow(`SELECT u.version_id,u.package_hash,s.active_version_id FROM work_skillhub_uploads u JOIN agent_skills s ON s.id=u.skill_id`).Scan(&recordedID, &hash, &currentID)
	sum := sha256.Sum256(published)
	if err != nil || recordedID != originalID || currentID != nextID || hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("receipt does not identify published snapshot: recorded=%v current=%v hash=%q err=%v", recordedID, currentID, hash, err)
	}
}
