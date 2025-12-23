package httpserver

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"db_inner_migrator_syncer/internal/audit"
	"db_inner_migrator_syncer/internal/auth"
	"db_inner_migrator_syncer/internal/rbac"
)

type AuthMiddleware struct {
	authenticator auth.Authenticator
	pool          *pgxpool.Pool
	logger        audit.Logger
}

func NewAuthMiddleware(authenticator auth.Authenticator, pool *pgxpool.Pool, logger audit.Logger) *AuthMiddleware {
	return &AuthMiddleware{authenticator: authenticator, pool: pool, logger: logger}
}

func (m *AuthMiddleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := m.authenticator.Authenticate(r)
		if err != nil {
			if !errors.Is(err, auth.ErrUnauthorized) {
				m.logger.Error("auth error", "error", err)
			}
			m.logDenied(r.Context(), nil, "unauthenticated", r)
			writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		if user == nil {
			m.logDenied(r.Context(), nil, "unauthenticated", r)
			writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		ctx := auth.WithUser(r.Context(), user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *AuthMiddleware) RequireRoles(roles ...rbac.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := auth.UserFromContext(r.Context())
			if !ok {
				writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
				return
			}
			if !rbac.Allows(user.Role, roles...) {
				m.logDenied(r.Context(), user, "insufficient_role", r)
				writeError(w, http.StatusForbidden, "forbidden", "insufficient role")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (m *AuthMiddleware) logDenied(ctx context.Context, user *auth.User, reason string, r *http.Request) {
	var actorID *uuid.UUID
	if user != nil {
		actorID = &user.ID
	}
	_ = audit.LogEvent(ctx, m.pool, m.logger, audit.Event{
		ActorID:    actorID,
		Action:     "access_denied",
		EntityType: "http_request",
		Payload: map[string]any{
			"path":   r.URL.Path,
			"method": r.Method,
			"reason": reason,
		},
	})
}
