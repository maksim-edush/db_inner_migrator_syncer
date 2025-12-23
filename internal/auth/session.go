package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/securecookie"

	"db_inner_migrator_syncer/internal/rbac"
)

const (
	SessionCookieName   = "migratehub_session"
	CSRFCookieName      = "migratehub_csrf"
	OIDCStateCookieName = "migratehub_oidc"
)

type Session struct {
	UserID    uuid.UUID
	Role      rbac.Role
	Email     string
	CSRFToken string
	ProjectID *uuid.UUID
	Flash     *FlashMessage
}

type FlashMessage struct {
	Kind    string
	Message string
}

type SessionManager struct {
	cookie *securecookie.SecureCookie
}

func NewSessionManager(secretKey []byte) *SessionManager {
	sc := securecookie.New(secretKey, secretKey)
	sc.MaxAge(int((24 * time.Hour * 7).Seconds()))
	sc.SetSerializer(securecookie.JSONEncoder{})
	return &SessionManager{cookie: sc}
}

func (s *SessionManager) SetSession(w http.ResponseWriter, session Session) error {
	encoded, err := s.cookie.Encode(SessionCookieName, session)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    encoded,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    session.CSRFToken,
		Path:     "/",
		HttpOnly: false,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *SessionManager) ClearSession(w http.ResponseWriter) {
	expire := time.Now().Add(-time.Hour)
	for _, name := range []string{SessionCookieName, CSRFCookieName, OIDCStateCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			Expires:  expire,
			MaxAge:   -1,
			HttpOnly: name != CSRFCookieName,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})
	}
}

func (s *SessionManager) GetSession(r *http.Request) (*Session, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return nil, err
	}
	var session Session
	if err := s.cookie.Decode(SessionCookieName, cookie.Value, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *SessionManager) Encode(name string, value any) (string, error) {
	return s.cookie.Encode(name, value)
}

func (s *SessionManager) Decode(name, value string, dst any) error {
	return s.cookie.Decode(name, value, dst)
}

func RandomToken(n int) (string, error) {
	if n <= 0 {
		return "", errors.New("invalid token length")
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
