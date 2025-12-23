package auth

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"db_inner_migrator_syncer/internal/store"
)

type SessionAuthenticator struct {
	sessions *SessionManager
	pool     *pgxpool.Pool
}

func NewSessionAuthenticator(sessions *SessionManager, pool *pgxpool.Pool) *SessionAuthenticator {
	return &SessionAuthenticator{sessions: sessions, pool: pool}
}

func (a *SessionAuthenticator) Authenticate(r *http.Request) (*User, error) {
	session, err := a.sessions.GetSession(r)
	if err != nil {
		return nil, ErrUnauthorized
	}
	user, err := store.GetUserByID(r.Context(), a.pool, session.UserID)
	if err != nil {
		if errors.Is(err, store.ErrUserDisabled) || errors.Is(err, store.ErrUserNotFound) {
			return nil, ErrUnauthorized
		}
		return nil, err
	}
	return &User{
		ID:        user.ID,
		Email:     user.Email,
		Name:      user.Name,
		Role:      user.Role,
		CSRFToken: session.CSRFToken,
		ProjectID: session.ProjectID,
	}, nil
}

type MultiAuthenticator struct {
	authenticators []Authenticator
}

func NewMultiAuthenticator(authenticators ...Authenticator) *MultiAuthenticator {
	return &MultiAuthenticator{authenticators: authenticators}
}

func (m *MultiAuthenticator) Authenticate(r *http.Request) (*User, error) {
	var lastErr error
	for _, a := range m.authenticators {
		user, err := a.Authenticate(r)
		if err == nil && user != nil {
			return user, nil
		}
		if err != nil && !errors.Is(err, ErrUnauthorized) {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, ErrUnauthorized
}

// Dummy compiler check
var _ Authenticator = (*SessionAuthenticator)(nil)
var _ Authenticator = (*MultiAuthenticator)(nil)
