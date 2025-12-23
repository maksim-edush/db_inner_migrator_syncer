package auth

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"db_inner_migrator_syncer/internal/rbac"
)

var ErrUnauthorized = errors.New("unauthorized")

type Authenticator interface {
	Authenticate(r *http.Request) (*User, error)
}

// DevHeaderAuthenticator is a development-only authenticator that trusts headers.
type DevHeaderAuthenticator struct {
	enabled bool
}

func NewDevHeaderAuthenticator(enabled bool) *DevHeaderAuthenticator {
	return &DevHeaderAuthenticator{enabled: enabled}
}

func (a *DevHeaderAuthenticator) Authenticate(r *http.Request) (*User, error) {
	if !a.enabled {
		return nil, ErrUnauthorized
	}
	email := strings.TrimSpace(r.Header.Get("X-MigrateHub-Email"))
	role := strings.TrimSpace(r.Header.Get("X-MigrateHub-Role"))
	name := r.Header.Get("X-MigrateHub-Name")

	if email == "" || role == "" {
		return nil, ErrUnauthorized
	}

	token, err := RandomToken(16)
	if err != nil {
		return nil, ErrUnauthorized
	}

	return &User{
		ID:        uuid.New(),
		Email:     email,
		Name:      strings.TrimSpace(name),
		Role:      rbac.Role(strings.ToLower(role)),
		CSRFToken: token,
		ProjectID: nil,
	}, nil
}
