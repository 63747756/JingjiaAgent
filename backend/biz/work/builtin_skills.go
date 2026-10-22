package work

import (
	"context"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/google/uuid"
	"log/slog"
	"time"
)

func (s *Service) ensureBuiltinSkills(ctx context.Context) error {
	packages, err := skillPackages()
	if err != nil {
		return err
	}
	ids := []string{}
	for _, p := range packages {
		ids = append(ids, p.ID)
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT t.id,(SELECT m.user_id FROM team_members m JOIN users u ON u.id=m.user_id WHERE m.team_id=t.id AND u.deleted_at IS NULL ORDER BY (m.role='admin') DESC,m.user_id LIMIT 1) FROM teams t WHERE t.deleted_at IS NULL`)
	if err != nil {
		return err
	}
	type pair struct{ team, uid uuid.UUID }
	targets := []pair{}
	for rows.Next() {
		var p pair
		var uid *uuid.UUID
		if err = rows.Scan(&p.team, &uid); err != nil {
			rows.Close()
			return err
		}
		if uid != nil {
			p.uid = *uid
			targets = append(targets, p)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range targets {
		if _, err = s.importSkills(ctx, &domain.User{ID: p.uid}, SkillImportRequest{TeamID: p.team, PackageIDs: ids}, true); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) startBuiltinSkills() {
	if s.cfg == nil || !s.cfg.Work.Enabled || s.ent == nil {
		return
	}
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.ensureBuiltinSkills(ctx); err != nil {
			slog.Warn("builtin skills reconciliation failed; will retry")
		}
	}
	run()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			run()
		}
	}()
}
