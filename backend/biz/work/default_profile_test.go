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

func TestDefaultProfileRequiresTeamAdministrator(t *testing.T) {
	s, m := mockedService(t)
	uid, team, profile := uuid.New(), uuid.New(), uuid.New()
	m.ExpectQuery("SELECT EXISTS.*team_members").WithArgs(uid, team, true).WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(false))
	if err := s.SetDefaultProfile(context.Background(), &domain.User{ID: uid}, DefaultProfileRequest{TeamID: team, ProfileID: &profile}); !errors.Is(err, errcode.ErrForbidden) {
		t.Fatalf("non-administrator default selection accepted: %v", err)
	}
}

func TestDefaultProfileValidatesEnabledTeamAssistantAndCommitsAtomically(t *testing.T) {
	for _, mode := range []string{"select", "clear", "disabled", "foreign", "update failure", "selection failure"} {
		t.Run(mode, func(t *testing.T) {
			s, m := mockedService(t)
			uid, team, profile := uuid.New(), uuid.New(), uuid.New()
			req := DefaultProfileRequest{TeamID: team, ProfileID: &profile}
			if mode == "clear" {
				req.ProfileID = nil
			}
			m.ExpectQuery("SELECT EXISTS.*team_members").WithArgs(uid, team, true).WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(true))
			m.ExpectBegin()
			m.ExpectQuery("SELECT id FROM teams.*FOR UPDATE").WithArgs(team).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(team))
			if req.ProfileID != nil {
				rows := sqlmock.NewRows([]string{"enabled"})
				if mode != "foreign" {
					rows.AddRow(mode != "disabled")
				}
				m.ExpectQuery("SELECT enabled FROM work_execution_profiles WHERE id=.*AND team_id=").WithArgs(profile, team).WillReturnRows(rows)
			}
			if mode == "disabled" || mode == "foreign" {
				m.ExpectRollback()
			} else {
				update := m.ExpectExec("UPDATE work_execution_profiles SET is_default=false.*WHERE team_id=.*AND is_default AND id IS DISTINCT FROM").WithArgs(team, req.ProfileID)
				if mode == "update failure" {
					update.WillReturnError(errors.New("write failure"))
					m.ExpectRollback()
				} else {
					update.WillReturnResult(sqlmock.NewResult(0, 2))
					if req.ProfileID != nil {
						selection := m.ExpectExec("UPDATE work_execution_profiles SET is_default=true.*WHERE id=.*AND team_id=.*AND enabled AND NOT is_default").WithArgs(profile, team)
						if mode == "selection failure" {
							selection.WillReturnError(errors.New("selection failure"))
							m.ExpectRollback()
						} else {
							selection.WillReturnResult(sqlmock.NewResult(0, 1))
						}
					}
					if mode != "selection failure" {
						m.ExpectCommit()
					}
				}
			}
			err := s.SetDefaultProfile(context.Background(), &domain.User{ID: uid}, req)
			if mode == "select" || mode == "clear" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("invalid or failed selection was accepted")
			}
		})
	}
}

func TestEditingProfilePreservesLatestDefaultAndDisablingClearsIt(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		new, enabled, savedDefault bool
		staleDefault, want         bool
	}{
		{"new assistant", true, true, false, true, false},
		{"old default was replaced", false, true, false, true, false},
		{"assistant became default", false, true, true, false, true},
		{"default assistant disabled", false, false, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, m := mockedService(t)
			p := Profile{ID: uuid.New(), TeamID: uuid.New(), Enabled: tc.enabled, IsDefault: tc.staleDefault}
			if tc.new {
				p.ID = uuid.Nil
			}
			m.ExpectBegin()
			tx, err := s.store.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.new {
				m.ExpectQuery("SELECT is_default FROM work_execution_profiles WHERE id=.*AND team_id=").WithArgs(p.ID, p.TeamID).WillReturnRows(sqlmock.NewRows([]string{"is_default"}).AddRow(tc.savedDefault))
			}
			if err := retainDefaultProfile(context.Background(), tx, &p); err != nil || p.IsDefault != tc.want {
				t.Fatalf("default=%v want=%v error=%v", p.IsDefault, tc.want, err)
			}
			m.ExpectRollback()
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
