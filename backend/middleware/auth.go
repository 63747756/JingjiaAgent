package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/pkg/session"
)

const (
	UserContextKey     = "user"
	TeamUserContextKey = "team_user"
)

// GetUser 从上下文中获取用户信息
func GetUser(ctx echo.Context) *domain.User {
	user, ok := ctx.Get(UserContextKey).(*domain.User)
	if !ok {
		return nil
	}
	return user
}

// SetUser 设置用户信息到上下文
func SetUser(ctx echo.Context, user *domain.User) {
	ctx.Set(UserContextKey, user)
}

// GetTeamUser 从上下文中获取团队用户信息
func GetTeamUser(ctx echo.Context) *domain.TeamUser {
	user, ok := ctx.Get(TeamUserContextKey).(*domain.TeamUser)
	if !ok {
		return nil
	}
	return user
}

// SetTeamUser 设置团队用户信息到上下文
func SetTeamUser(ctx echo.Context, user *domain.TeamUser) {
	ctx.Set(TeamUserContextKey, user)
}

// TeamAdminAuth 团队管理员权限中间件，必须在 TeamAuth 之后使用
func TeamAdminAuth(isAdmin func(ctx context.Context, teamID, userID uuid.UUID) bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			teamUser := GetTeamUser(c)
			if teamUser == nil || teamUser.User == nil || teamUser.Team == nil {
				return c.String(http.StatusForbidden, "Forbidden")
			}

			if !isAdmin(c.Request().Context(), teamUser.GetTeamID(), teamUser.User.ID) {
				return c.String(http.StatusForbidden, "Forbidden")
			}

			return next(c)
		}
	}
}

// AuthMiddleware 认证中间件管理器
type AuthMiddleware struct {
	Session *session.Session
	usecase domain.UserUsecase
	logger  *slog.Logger
}

// NewAuthMiddleware 创建认证中间件管理器
func NewAuthMiddleware(
	sess *session.Session,
	usecase domain.UserUsecase,
	logger *slog.Logger,
) *AuthMiddleware {
	return &AuthMiddleware{
		Session: sess,
		usecase: usecase,
		logger:  logger.With("module", "AuthMiddleware"),
	}
}

// currentSessionUser checks local account state on every authorization. Removing
// cached sessions alone cannot prevent a concurrent login from issuing a new one
// after an administrator disables or deletes the account. This never queries AD.
// invalid is distinct from a temporary database failure, which must not revoke
// otherwise valid sessions.
func (a *AuthMiddleware) currentSessionUser(ctx context.Context, cached *domain.User, teamSession bool) (current *domain.User, invalid bool, err error) {
	if cached == nil || cached.ID == uuid.Nil {
		return nil, true, nil
	}
	if a.usecase == nil {
		return nil, false, fmt.Errorf("local account lookup unavailable")
	}
	current, err = a.usecase.Get(ctx, cached.ID)
	if err != nil {
		if db.IsNotFound(err) {
			return nil, true, nil
		}
		return nil, false, err
	}
	if !validCurrentUser(current, cached) {
		return nil, true, nil
	}
	if cached.Team == nil {
		if teamSession {
			return nil, true, nil
		}
		return current, false, nil
	}
	info, err := a.usecase.GetUserWithTeams(ctx, cached.ID)
	if err != nil {
		if db.IsNotFound(err) {
			return nil, true, nil
		}
		return nil, false, err
	}
	if info == nil || !validCurrentUser(info.User, cached) {
		return nil, true, nil
	}
	for _, member := range info.Teams {
		if member == nil || member.TeamID != cached.Team.ID || member.UserID != cached.ID {
			continue
		}
		current = info.User
		current.Team = &domain.Team{ID: member.TeamID, Name: member.TeamName}
		return current, false, nil
	}
	return nil, true, nil
}

func validCurrentUser(current, cached *domain.User) bool {
	return current != nil && current.ID == cached.ID && current.Status == consts.UserStatusActive && !current.IsBlocked && current.Role == cached.Role
}

func (a *AuthMiddleware) revokeInvalidSessions(ctx context.Context, userID uuid.UUID) {
	if userID == uuid.Nil {
		return
	}
	for _, name := range []string{consts.JingjiaAgentAISession, consts.JingjiaAgentAITeamSession} {
		if err := a.Session.Trunc(ctx, name, userID); err != nil {
			a.logger.WarnContext(ctx, "revoke invalid local account session failed", "user_id", userID)
		}
	}
}

// Auth 强制要求认证
func (a *AuthMiddleware) Auth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()

			user, err := session.Get[*domain.User](a.Session, c, consts.JingjiaAgentAISession)
			if err != nil {
				a.logger.DebugContext(ctx, "get user session failed", "error", err)
				return c.String(http.StatusUnauthorized, "Unauthorized")
			}

			if user == nil {
				a.logger.DebugContext(ctx, "no user found, skipping auth")
				return c.String(http.StatusUnauthorized, "Unauthorized")
			}

			current, invalid, err := a.currentSessionUser(ctx, user, false)
			if err != nil {
				a.logger.WarnContext(ctx, "local account validation unavailable", "user_id", user.ID)
				return c.String(http.StatusServiceUnavailable, "Authentication temporarily unavailable")
			}
			if invalid {
				a.revokeInvalidSessions(ctx, user.ID)
				return c.String(http.StatusUnauthorized, "Unauthorized")
			}
			SetUser(c, current)
			return next(c)
		}
	}
}

// Check 检查用户是否已认证（不强制要求认证）
func (a *AuthMiddleware) Check() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()

			user, err := session.Get[*domain.User](a.Session, c, consts.JingjiaAgentAISession)
			if err != nil {
				a.logger.DebugContext(ctx, "get user session failed", "error", err)
				return next(c)
			}

			if user == nil {
				a.logger.DebugContext(ctx, "no user found, skipping auth")
				return next(c)
			}

			current, invalid, err := a.currentSessionUser(ctx, user, false)
			if err != nil {
				a.logger.WarnContext(ctx, "local account validation unavailable", "user_id", user.ID)
				return c.String(http.StatusServiceUnavailable, "Authentication temporarily unavailable")
			}
			if invalid {
				a.revokeInvalidSessions(ctx, user.ID)
				return next(c)
			}
			SetUser(c, current)
			return next(c)
		}
	}
}

// TeamAuth 团队认证中间件
func (a *AuthMiddleware) TeamAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()

			user, err := session.Get[*domain.User](a.Session, c, consts.JingjiaAgentAITeamSession)
			if err != nil {
				a.logger.DebugContext(ctx, "get team session failed", "error", err)
				return c.String(http.StatusUnauthorized, "Unauthorized")
			}

			if user == nil {
				return c.String(http.StatusUnauthorized, "Unauthorized")
			}

			current, invalid, err := a.currentSessionUser(ctx, user, true)
			if err != nil {
				a.logger.WarnContext(ctx, "local team account validation unavailable", "user_id", user.ID)
				return c.String(http.StatusServiceUnavailable, "Authentication temporarily unavailable")
			}
			if invalid {
				a.revokeInvalidSessions(ctx, user.ID)
				return c.String(http.StatusUnauthorized, "Unauthorized")
			}

			SetTeamUser(c, &domain.TeamUser{
				User: current,
				Team: current.Team,
			})
			return next(c)
		}
	}
}

// TeamAuthCheck 团队认证中间件（不强制）
func (a *AuthMiddleware) TeamAuthCheck() echo.MiddlewareFunc {
	// This route historically requires a valid team session, just like
	// TeamAuth; share its local state validation as well.
	return a.TeamAuth()
}
