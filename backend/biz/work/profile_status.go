package work

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
)

type ProfileStatusRequest struct {
	ID      uuid.UUID `param:"id" json:"-"`
	TeamID  uuid.UUID `json:"team_id"`
	Enabled *bool     `json:"enabled"`
}

type ProfileStatusResponse struct {
	ID        uuid.UUID `json:"id"`
	Enabled   bool      `json:"enabled"`
	IsDefault bool      `json:"is_default"`
}

func (s *Service) SetProfileStatus(ctx context.Context, user *domain.User, req ProfileStatusRequest) (ProfileStatusResponse, error) {
	out := ProfileStatusResponse{ID: req.ID}
	if err := s.enabled(); err != nil {
		return out, err
	}
	if req.ID == uuid.Nil || req.Enabled == nil {
		return out, errcode.ErrBadRequest
	}
	if err := s.teamPermission(ctx, user.ID, req.TeamID, true); err != nil {
		return out, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var teamID uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT id FROM teams WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, req.TeamID).Scan(&teamID); err != nil {
		return out, err
	}
	var presets []byte
	err = tx.QueryRowContext(ctx, `SELECT task_presets FROM work_execution_profiles WHERE id=$1 AND team_id=$2`, req.ID, req.TeamID).Scan(&presets)
	if errors.Is(err, sql.ErrNoRows) {
		return out, errcode.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if *req.Enabled {
		p := Profile{Enabled: true}
		if err := json.Unmarshal(presets, &p.TaskPresets); err != nil {
			return out, errcode.ErrBadRequest
		}
		if err := validateTaskPresets(&p); err != nil {
			return out, err
		}
	}
	// Availability is independent of configuration. Disabling clears the default;
	// re-enabling never restores it or overwrites instructions/resources/presets.
	err = tx.QueryRowContext(ctx, `UPDATE work_execution_profiles SET enabled=$3,is_default=(is_default AND enabled AND $3),updated_at=now() WHERE id=$1 AND team_id=$2 RETURNING enabled,is_default`, req.ID, req.TeamID, *req.Enabled).Scan(&out.Enabled, &out.IsDefault)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
