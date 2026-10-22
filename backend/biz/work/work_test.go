package work

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func mockedService(t *testing.T) (*Service, sqlmock.Sqlmock) {
	t.Helper()
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		conn.Close()
	})
	cfg := &config.Config{}
	cfg.Work.Enabled = true
	r := &Repository{db: conn}
	return &Service{cfg: cfg, store: r, guard: &AccessGuard{store: r}}, mock
}

type memorySkills struct{ data map[string][]byte }

func (m *memorySkills) GetObject(_ context.Context, key string) (io.ReadCloser, error) {
	b, ok := m.data[key]
	if !ok {
		return nil, errors.New("missing object")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func (m *memorySkills) PresignGet(_ context.Context, key string, _ time.Duration) (string, error) {
	if _, ok := m.data[key]; !ok {
		return "", errors.New("missing object")
	}
	return "https://objects.internal/" + key, nil
}
func (m *memorySkills) PutFile(_ context.Context, prefix, name string, r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.data[prefix+"/"+name] = b
	return nil
}

func TestWorkAccessOwnerAndCurrentGrants(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		foreign, legacy                  bool
		team, project, skills, resources bool
		want                             error
	}{
		{"legacy", false, true, false, false, false, false, nil},
		{"private", true, false, true, true, true, true, errcode.ErrNotFound},
		{"team removed", false, false, false, true, true, true, errcode.ErrForbidden},
		{"project removed", false, false, true, false, true, true, errcode.ErrForbidden},
		{"disabled skill keeps history", false, false, true, true, false, true, nil},
		{"input grant removed", false, false, true, true, true, false, errcode.ErrForbidden},
		{"authorized", false, false, true, true, true, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, m := mockedService(t)
			uid, tid, team, pid := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			owner := uid
			if tc.foreign {
				owner = uuid.New()
			}
			rows := sqlmock.NewRows([]string{"owner_id", "team_id", "project_id", "deleted"})
			if !tc.legacy {
				rows.AddRow(owner, team, pid.String(), false)
			}
			m.ExpectQuery("SELECT w.owner_id,w.team_id").WithArgs(tid).WillReturnRows(rows)
			if !tc.foreign && !tc.legacy {
				m.ExpectQuery("SELECT EXISTS.*team_members").WithArgs(uid, team).WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(tc.team))
				if tc.team {
					m.ExpectQuery("SELECT EXISTS.*projects").WithArgs(pid.String(), uid, true).WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(tc.project))
					if tc.project {
						m.ExpectQuery("SELECT NOT EXISTS.*work_tasks.*resource_manifest").WithArgs(tid, uid).WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(tc.resources))
					}
				}
			}
			if err := s.guard.CheckWorkTaskAccess(context.Background(), uid, tid, true); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}

func TestWorkDeletedTaskCannotFallThroughAsLegacy(t *testing.T) {
	s, m := mockedService(t)
	uid, team, tid := uuid.New(), uuid.New(), uuid.New()
	m.ExpectQuery("SELECT w.owner_id,w.team_id").WithArgs(tid).WillReturnRows(sqlmock.NewRows([]string{"owner_id", "team_id", "project_id", "deleted"}).AddRow(uid, team, nil, true))
	if err := s.guard.CheckWorkTaskAccess(context.Background(), uid, tid, false); !errors.Is(err, errcode.ErrNotFound) {
		t.Fatalf("deleted work task considered legacy: %v", err)
	}
}
func TestWorkIdempotencyRejectsChangedRequest(t *testing.T) {
	s, m := mockedService(t)
	uid, rid, tid := uuid.New(), uuid.New(), uuid.New()
	m.ExpectQuery("SELECT task_id,request_payload_hash").WithArgs(uid, rid).WillReturnRows(sqlmock.NewRows([]string{"task_id", "request_payload_hash"}).AddRow(tid, "original"))
	if _, err := s.store.priorRequest(context.Background(), uid, rid, "changed"); !errors.Is(err, errcode.ErrBadRequest) {
		t.Fatalf("changed replay accepted: %v", err)
	}
}
func TestWorkReviewReadsSubmittedVersionAndVerifiesHash(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "submitted", true: "corrupt"}[corrupt], func(t *testing.T) {
			s, m := mockedService(t)
			u := &domain.User{ID: uuid.New()}
			rid, sid, vid, team := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			data, _ := zipMarkdown("submitted content")
			digest := sha256.Sum256(data)
			hash := hex.EncodeToString(digest[:])
			if corrupt {
				hash = strings.Repeat("0", 64)
			}
			s.objects = &memorySkills{map[string][]byte{"submitted.zip": data, "latest.zip": []byte("latest must never be read")}}
			m.ExpectQuery("SELECT EXISTS.*team_members").WithArgs(u.ID, team, true).WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(true))
			m.ExpectQuery("SELECT r.id,r.skill_id,r.version_id.*v.id=r.version_id").WithArgs(rid, team).WillReturnRows(sqlmock.NewRows([]string{"id", "skill_id", "version_id", "team_id", "state", "note", "name", "submitter", "hash", "key", "created"}).AddRow(rid, sid, vid, team, "pending", "", "fixture", uuid.New(), hash, "submitted.zip", time.Now()))
			out, err := s.ReviewDetail(context.Background(), u, rid, team)
			if corrupt {
				if !errors.Is(err, errcode.ErrBadRequest) {
					t.Fatalf("corrupt package accepted: %v", err)
				}
				return
			}
			if err != nil || out.VersionID != vid || out.Content != "submitted content" {
				t.Fatalf("submitted snapshot: %#v %v", out, err)
			}
		})
	}
}
func TestWorkImportRejectsForeignTeamAndNonReleaseContent(t *testing.T) {
	s, m := mockedService(t)
	uid, team := uuid.New(), uuid.New()
	m.ExpectQuery("SELECT EXISTS.*team_members").WithArgs(uid, team, true).WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(false))
	if _, err := s.ImportSkills(context.Background(), &domain.User{ID: uid}, SkillImportRequest{TeamID: team, PackageIDs: []string{"enterprise-work-general"}}); !errors.Is(err, errcode.ErrForbidden) {
		t.Fatalf("foreign import: %v", err)
	}
	m.ExpectQuery("SELECT EXISTS.*team_members").WithArgs(uid, team, true).WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(true))
	if _, err := s.ImportSkills(context.Background(), &domain.User{ID: uid}, SkillImportRequest{TeamID: team, PackageIDs: []string{"https://evil/skill.zip"}}); !errors.Is(err, errcode.ErrBadRequest) {
		t.Fatalf("arbitrary package: %v", err)
	}
}
func TestWorkReleasePackagesAndInternalModelAllowlist(t *testing.T) {
	packages, err := skillPackages()
	if err != nil || len(packages) != 4 {
		t.Fatalf("release: %v %v", packages, err)
	}
	for _, p := range packages {
		data, err := verifiedReleasePackage(p)
		if err != nil {
			t.Fatalf("verified release %s: %v", p.ID, err)
		}
		archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		entry, err := archive.Open("SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		content, readErr := io.ReadAll(entry)
		closeErr := entry.Close()
		if readErr != nil || closeErr != nil || !bytes.Contains(content, []byte("/opt/office/venv/bin/python")) {
			t.Fatalf("release %s must use the fixed Office interpreter: read=%v close=%v", p.ID, readErr, closeErr)
		}
		p.SHA256 = strings.Repeat("0", 64)
		if _, err := verifiedReleasePackage(p); err == nil {
			t.Fatal("corrupt release hash accepted")
		}
	}
	for _, tc := range []struct {
		url     string
		hosts   []string
		allowed bool
	}{
		{"http://model.internal:8000/v1", []string{"model.internal:8000"}, true},
		{"https://MODEL.internal/v1", []string{"model.internal:443"}, true},
		{"http://[::1]:8000/v1", []string{"[::1]:8000"}, true},
		{"http://model.internal:8001/v1", []string{"model.internal:8000"}, false},
		{"https://model.internal.evil/v1", []string{"model.internal:443"}, false},
		{"https://user:secret@model.internal/v1", []string{"model.internal:443"}, false},
		{"http://model.internal/v1", nil, false},
	} {
		if err := validateOfflineModel(tc.url, tc.hosts); (err == nil) != tc.allowed {
			t.Fatalf("allowlist %s: %v", tc.url, err)
		}
	}
	profile := Profile{Name: "默认", TeamID: uuid.New(), ModelID: uuid.New(), ImageID: uuid.New(), HostID: "node", TaskPresets: []TaskPreset{{ID: "task", Name: "任务", Description: "根据目标完成任务", Icon: "sparkles", Enabled: true}}}
	if err = validateProfile(&profile); err != nil {
		t.Fatalf("default profile: %v %v", profile, err)
	}
	pin := SkillPin{SkillID: uuid.New(), VersionID: uuid.New(), S3Key: "secret-storage-key"}
	public, _ := json.Marshal(pin)
	stored, _ := marshalPins([]SkillPin{pin})
	if bytes.Contains(public, []byte(pin.S3Key)) || !bytes.Contains(stored, []byte(pin.S3Key)) {
		t.Fatal("storage key serialization boundary")
	}
}
