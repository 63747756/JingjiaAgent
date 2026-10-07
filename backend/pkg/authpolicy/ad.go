package authpolicy

import (
	"context"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/teamadconfig"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
)

func ADEnabled(ctx context.Context, client *db.Client) (bool, error) {
	return client.TeamADConfig.Query().Where(teamadconfig.EnabledEQ(true)).Exist(ctx)
}

func RequireNonADLogin(ctx context.Context, client *db.Client) error {
	enabled, err := ADEnabled(ctx, client)
	if err != nil {
		return err
	}
	if enabled {
		return errcode.ErrADDisabled
	}
	return nil
}

func RequireLocalPassword(ctx context.Context, client *db.Client, userID uuid.UUID) error {
	account, err := client.User.Get(ctx, userID)
	if err != nil {
		return err
	}
	if account.AuthSource == "ad" {
		return errcode.ErrADLocalPasswordDenied
	}
	return nil
}
