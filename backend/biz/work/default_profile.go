package work

import (
	"context"
	"database/sql"
	"errors"

	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
)

// Default selection is a team setting, independent of an assistant's editable
// configuration. A nil ProfileID explicitly clears the selection.
type DefaultProfileRequest struct {
	TeamID    uuid.UUID  `json:"team_id"`
	ProfileID *uuid.UUID `json:"profile_id"`
}

func (s *Service) SetDefaultProfile(ctx context.Context, user *domain.User, req DefaultProfileRequest) error {
	if err := s.enabled(); err != nil {
		return err
	}
	if req.ProfileID != nil && *req.ProfileID == uuid.Nil {
		return errcode.ErrBadRequest
	}
	if err := s.teamPermission(ctx, user.ID, req.TeamID, true); err != nil {
		return err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var teamID uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT id FROM teams WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, req.TeamID).Scan(&teamID); err != nil {
		return err
	}
	if req.ProfileID != nil {
		var enabled bool
		err = tx.QueryRowContext(ctx, `SELECT enabled FROM work_execution_profiles WHERE id=$1 AND team_id=$2`, *req.ProfileID, req.TeamID).Scan(&enabled)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !enabled) {
			return errcode.ErrBadRequest.Wrap(errors.New("请选择本团队已启用的助手"))
		}
		if err != nil {
			return err
		}
	}
	// Clear the old selection first: the partial unique index is immediate,
	// so a single UPDATE that flips both rows can violate it mid-statement.
	if _, err = tx.ExecContext(ctx, `UPDATE work_execution_profiles SET is_default=false,updated_at=now() WHERE team_id=$1 AND is_default AND id IS DISTINCT FROM $2::uuid`, req.TeamID, req.ProfileID); err != nil {
		return err
	}
	if req.ProfileID != nil {
		if _, err = tx.ExecContext(ctx, `UPDATE work_execution_profiles SET is_default=true,updated_at=now() WHERE id=$1 AND team_id=$2 AND enabled AND NOT is_default`, *req.ProfileID, req.TeamID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func retainDefaultProfile(ctx context.Context, tx *sql.Tx, p *Profile) error {
	// Creating an assistant never changes the team's default. When editing,
	// retain the latest database value rather than replaying a stale form value.
	p.IsDefault = false
	if p.ID == uuid.Nil {
		return nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT is_default FROM work_execution_profiles WHERE id=$1 AND team_id=$2`, p.ID, p.TeamID).Scan(&p.IsDefault); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errcode.ErrNotFound
		}
		return err
	}
	p.IsDefault = p.IsDefault && p.Enabled
	return nil
}
