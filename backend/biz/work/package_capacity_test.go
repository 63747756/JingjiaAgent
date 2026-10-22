package work

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/63747756/jingjiaagent/backend/db/agentpluginrepo"
	"github.com/63747756/jingjiaagent/backend/db/agentskill"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

func executablePackage(t *testing.T, name, prefix string, files int) []byte {
	t.Helper()
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for i := 0; i < files; i++ {
		filename, content, mode := fmt.Sprintf("assets/%04d.txt", i), "fixture", os.FileMode(0644)
		if i == 0 {
			filename, content = "SKILL.md", "---\nname: "+name+"\ndescription: test\n---\nRun ./scripts/run.sh\n"
		} else if i == 1 {
			filename, content, mode = "scripts/run.sh", "#!/bin/sh\nprintf 'executable-ok'\n", 0755
		}
		h := &zip.FileHeader{Name: prefix + filename, Method: zip.Deflate}
		h.SetMode(mode)
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(f, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestPersonalPackageRetainsExecutableScripts(t *testing.T) {
	for _, prefix := range []string{"", "example/"} {
		t.Run(prefix, func(t *testing.T) {
			original := executablePackage(t, "example", prefix, 3)
			normalized, _, err := normalizeSkillZip(original)
			if err != nil {
				t.Fatal(err)
			}
			edited, err := replaceSkillMarkdown(normalized, "---\nname: example\ndescription: edited\n---\nRun script\n")
			if err != nil {
				t.Fatal(err)
			}
			for _, data := range [][]byte{normalized, edited} {
				z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, f := range z.File {
					want := os.FileMode(0644)
					if f.Name == "scripts/run.sh" {
						want, found = 0755, true
					}
					if f.Mode().Perm() != want {
						t.Fatalf("%s mode: %v, want %v", f.Name, f.Mode(), want)
					}
					if f.Name == "scripts/run.sh" && runtime.GOOS != "windows" {
						body, err := f.Open()
						if err != nil {
							t.Fatal(err)
						}
						b, err := io.ReadAll(body)
						body.Close()
						if err != nil {
							t.Fatal(err)
						}
						name := filepath.Join(t.TempDir(), "run.sh")
						// Match the Guest's permission mask, not host unzip defaults.
						if err = os.WriteFile(name, b, 0600|(f.Mode().Perm()&0111)); err != nil {
							t.Fatal(err)
						}
						if out, err := exec.Command(name).Output(); err != nil || string(out) != "executable-ok" {
							t.Fatalf("script execution: %q %v", out, err)
						}
					}
				}
				if !found {
					t.Fatal("script lost")
				}
			}
		})
	}
}

func TestSkillSelectionRejectsAggregateFileCountBeforeAdmission(t *testing.T) {
	s, first, uid, team := skillFixture(t)
	ctx := context.Background()
	second := s.ent.AgentSkill.Create().SetRepoID(first.RepoID).SetName("second").
		SetScopeType(agentskill.ScopeTypeUser).SetScopeID(uid.String()).SetCreatedBy(uid).SaveX(ctx)
	version := s.ent.AgentSkillVersion.Create().SetResourceID(second.ID).SetVersion("v1").SetS3Key("second.zip").SaveX(ctx)
	s.ent.AgentSkill.UpdateOneID(second.ID).SetActiveVersionID(version.ID).SaveX(ctx)
	objects := s.objects.(*memorySkills)
	objects.data["old.zip"] = executablePackage(t, "fixture", "", 600)
	for _, count := range []int{400, 401, 600} {
		objects.data["second.zip"] = executablePackage(t, "second", "", count)
		_, _, err := s.pinSkills(ctx, uid, team, []string{first.ID.String(), second.ID.String()}, nil)
		if count == 400 && err != nil {
			t.Fatalf("exact Guest boundary rejected: %v", err)
		}
		if count > 400 && (!errors.Is(err, errcode.ErrBadRequest) || !strings.Contains(err.Error(), "1000")) {
			t.Fatalf("over-limit skills admitted: %v", err)
		}
	}
}

func TestRoundResourceCapacityIncludesPluginsAndPinsDigest(t *testing.T) {
	s, skill, uid, team := skillFixture(t)
	ctx := context.Background()
	objects := s.objects.(*memorySkills)
	objects.data["old.zip"] = executablePackage(t, "fixture", "", 600)
	r := &SkillResolver{ent: s.ent, objects: objects}
	snapshot, _, err := r.resolveProfile(ctx, uid, Profile{TeamID: team}, nil)
	if err != nil || len(snapshot.Pins) != 1 || snapshot.Pins[0].SkillID != skill.ID {
		t.Fatalf("snapshot: %v", err)
	}
	objects.data["plugin.zip"] = executablePackage(t, "plugin", "", 401)
	plugin := &taskflow.AgentResourceAssetRef{Name: "plugin", Version: "v1", S3Key: "plugin.zip"}
	req := &taskflow.CreateTaskReq{RoundSkills: snapshot, AgentResources: &taskflow.AgentResources{Plugins: []*taskflow.AgentResourceAssetRef{plugin}}}
	if err = r.ValidateRoundResources(ctx, req); !errors.Is(err, errcode.ErrBadRequest) {
		t.Fatalf("combined resources admitted: %v", err)
	}
	objects.data["plugin.zip"] = executablePackage(t, "plugin", "", 400)
	if err = r.ValidateRoundResources(ctx, req); err != nil || len(plugin.SHA256) != 64 {
		t.Fatalf("exact boundary/digest: %v", err)
	}
	objects.data["plugin.zip"] = executablePackage(t, "plugin", "", 399)
	if err = r.ValidateRoundResources(ctx, req); err == nil || !strings.Contains(err.Error(), "摘要") {
		t.Fatalf("changed pinned plugin accepted: %v", err)
	}
}

func TestHistoricalPluginUsesPinnedObjectInsteadOfActiveVersion(t *testing.T) {
	s, _, uid, _ := skillFixture(t)
	ctx := context.Background()
	repo := s.ent.AgentPluginRepo.Create().SetName("plugins").SetSourceType(agentpluginrepo.SourceTypeBare).SetCreatedBy(uid).SaveX(ctx)
	plugin := s.ent.AgentPlugin.Create().SetRepoID(repo.ID).SetName("legacy-plugin").SetCreatedBy(uid).SaveX(ctx)
	s.ent.AgentPluginVersion.Create().SetResourceID(plugin.ID).SetVersion("v1").SetS3Key("plugins/legacy-v1.zip").SaveX(ctx)
	v2 := s.ent.AgentPluginVersion.Create().SetResourceID(plugin.ID).SetVersion("v2").SetS3Key("plugins/new-v2.zip").SaveX(ctx)
	s.ent.AgentPlugin.UpdateOneID(plugin.ID).SetActiveVersionID(v2.ID).SaveX(ctx)
	s.objects.(*memorySkills).data["plugins/legacy-v1.zip"] = executablePackage(t, "legacy-plugin", "", 2)
	r := &SkillResolver{ent: s.ent, objects: s.objects}
	ref := &taskflow.AgentResourceAssetRef{Name: "legacy-plugin", Version: "v1", ZipURL: "https://storage.invalid/bucket/plugins/legacy-v1.zip?expired=true"}
	req := &taskflow.CreateTaskReq{RoundSkills: &taskflow.RoundSkillSnapshot{}, AgentResources: &taskflow.AgentResources{Plugins: []*taskflow.AgentResourceAssetRef{ref}}}
	if err := r.ValidateRoundResources(ctx, req); err != nil || ref.S3Key != "plugins/legacy-v1.zip" || len(ref.SHA256) != 64 {
		t.Fatalf("old pinned plugin not recovered: %+v %v", ref, err)
	}
	ref.S3Key = ""
	ref.ZipURL = "https://storage.invalid/bucket/unrelated.zip"
	if err := r.ValidateRoundResources(ctx, req); err == nil {
		t.Fatal("unrelated URL selected an active plugin version")
	}
}
