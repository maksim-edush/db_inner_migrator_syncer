package auth

import (
	"context"

	"github.com/google/uuid"

	"db_inner_migrator_syncer/internal/rbac"
)

type User struct {
	ID        uuid.UUID
	Email     string
	Name      string
	Role      rbac.Role
	CSRFToken string
	ProjectID *uuid.UUID
}

type contextKey string

const userKey contextKey = "migratehub-user"

func WithUser(ctx context.Context, user *User) context.Context {
	return context.WithValue(ctx, userKey, user)
}

func UserFromContext(ctx context.Context) (*User, bool) {
	val := ctx.Value(userKey)
	if val == nil {
		return nil, false
	}
	user, ok := val.(*User)
	return user, ok
}
