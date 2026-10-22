package work

import (
	"context"
	"errors"
	"testing"

	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestProfileStatusRequiresExplicitValueAndTeamAdministrator(t *testing.T) {
	s, m := mockedService(t)
	uid, team, id := uuid.New(), uuid.New(), uuid.New()
	if _, err := s.SetProfileStatus(context.Background(), &domain.User{ID: uid}, ProfileStatusRequest{ID: id, TeamID: team}); !errors.Is(err, errcode.ErrBadRequest) {
		t.Fatalf("missing enabled was accepted: %v", err)
	}
	enabled := true
	m.ExpectQuery("SELECT EXISTS.*team_members").WithArgs(uid, team, true).WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(false))
	if _, err := s.SetProfileStatus(context.Background(), &domain.User{ID: uid}, ProfileStatusRequest{ID: id, TeamID: team, Enabled: &enabled}); !errors.Is(err, errcode.ErrForbidden) {
		t.Fatalf("non-administrator status change accepted: %v", err)
	}
}

func TestProfileStatusChangesOnlyAvailabilityAndValidatesCurrentTasks(t *testing.T) {
	for _, mode := range []string{"enable", "disable default", "hidden tasks", "foreign", "write failure"} {
		t.Run(mode, func(t *testing.T) {
			s, m := mockedService(t)
			uid, team, id := uuid.New(), uuid.New(), uuid.New()
			enabled := mode != "disable default"
			m.ExpectQuery("SELECT EXISTS.*team_members").WithArgs(uid, team, true).WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(true))
			m.ExpectBegin()
			m.ExpectQuery("SELECT id FROM teams.*FOR UPDATE").WithArgs(team).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(team))
			rows := sqlmock.NewRows([]string{"task_presets"})
			if mode != "foreign" {
				presets := `[{"id":"data","name":"数据分析","description":"核对资料","icon":"chart-column","enabled":true}]`
				if mode == "hidden tasks" || mode == "disable default" {
					presets = `[{"id":"data","name":"数据分析","description":"核对资料","icon":"chart-column","enabled":false}]`
				}
				rows.AddRow([]byte(presets))
			}
			m.ExpectQuery("SELECT task_presets FROM work_execution_profiles WHERE id=.*AND team_id=").WithArgs(id, team).WillReturnRows(rows)
			if mode == "hidden tasks" || mode == "foreign" {
				m.ExpectRollback()
			} else {
				update := m.ExpectQuery(`UPDATE work_execution_profiles SET enabled=.*is_default=.*updated_at=.*WHERE id=.*AND team_id=.*RETURNING enabled,is_default`).WithArgs(id, team, enabled)
				if mode == "write failure" {
					update.WillReturnError(errors.New("write failure"))
					m.ExpectRollback()
				} else {
					update.WillReturnRows(sqlmock.NewRows([]string{"enabled", "is_default"}).AddRow(enabled, false))
					m.ExpectCommit()
				}
			}
			out, err := s.SetProfileStatus(context.Background(), &domain.User{ID: uid}, ProfileStatusRequest{ID: id, TeamID: team, Enabled: &enabled})
			if mode == "enable" || mode == "disable default" {
				if err != nil || out.ID != id || out.Enabled != enabled || out.IsDefault {
					t.Fatalf("status/default transition: %#v %v", out, err)
				}
			} else if err == nil {
				t.Fatal("invalid or failed status change was accepted")
			}
		})
	}
}
