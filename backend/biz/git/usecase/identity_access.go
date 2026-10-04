package usecase

import (
	"context"

	"github.com/google/uuid"

	"github.com/chaitin/MonkeyCode/backend/db"
	"github.com/chaitin/MonkeyCode/backend/domain"
	"github.com/chaitin/MonkeyCode/backend/errcode"
)

// UserIdentity checks ownership before any token lookup, including cache hits.
func UserIdentity(ctx context.Context, repo domain.GitIdentityRepo, userID, identityID uuid.UUID) (*db.GitIdentity, error) {
	if userID == uuid.Nil || identityID == uuid.Nil {
		return nil, errcode.ErrNotFound
	}
	identity, err := repo.GetByUserID(ctx, userID, identityID)
	if err != nil {
		if db.IsNotFound(err) {
			return nil, errcode.ErrNotFound
		}
		return nil, err
	}
	if identity == nil || identity.ID != identityID || identity.UserID != userID || !identity.DeletedAt.IsZero() {
		return nil, errcode.ErrNotFound
	}
	return identity, nil
}

// ProjectIdentity is used only after the caller has authorized project access.
// Collaborators retain access to the project's owner's identity, but an invalid
// historical project binding cannot grant access to another owner's credential.
func ProjectIdentity(project *db.Project) (*db.GitIdentity, error) {
	if project == nil || project.UserID == uuid.Nil || project.GitIdentityID == uuid.Nil || !project.DeletedAt.IsZero() {
		return nil, errcode.ErrNotFound
	}
	identity := project.Edges.GitIdentity
	if identity == nil || identity.ID != project.GitIdentityID || identity.UserID != project.UserID || !identity.DeletedAt.IsZero() {
		return nil, errcode.ErrNotFound
	}
	return identity, nil
}

func (p *TokenProvider) GetTokenForUser(ctx context.Context, userID, identityID uuid.UUID) (*db.GitIdentity, string, error) {
	identity, err := UserIdentity(ctx, p.repo, userID, identityID)
	if err != nil {
		return nil, "", err
	}
	token, err := p.GetToken(ctx, identityID)
	return identity, token, err
}
