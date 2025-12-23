package httpserver

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"db_inner_migrator_syncer/internal/audit"
	"db_inner_migrator_syncer/internal/auth"
	"db_inner_migrator_syncer/internal/config"
	"db_inner_migrator_syncer/internal/store"
)

type AuthHandler struct {
	cfg           config.Config
	logger        requestLogger
	oidc          *auth.OIDCProvider
	sessions      *auth.SessionManager
	pool          *pgxpool.Pool
	autoProvision bool
}

func NewAuthHandler(cfg config.Config, logger requestLogger, oidcProvider *auth.OIDCProvider, sessions *auth.SessionManager, pool *pgxpool.Pool) *AuthHandler {
	return &AuthHandler{
		cfg:           cfg,
		logger:        logger,
		oidc:          oidcProvider,
		sessions:      sessions,
		pool:          pool,
		autoProvision: cfg.OIDC.AutoProvision,
	}
}

type oidcState struct {
	State string
	Nonce string
}

func (h *AuthHandler) GoogleStart(w http.ResponseWriter, r *http.Request) {
	state, err := auth.RandomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "oidc_state_error", "failed to generate state")
		return
	}
	nonce, err := auth.RandomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "oidc_nonce_error", "failed to generate nonce")
		return
	}

	encoded, err := h.sessions.Encode(auth.OIDCStateCookieName, oidcState{State: state, Nonce: nonce})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "oidc_state_error", "failed to persist state")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.OIDCStateCookieName,
		Value:    encoded,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	authURL := h.oidc.AuthCodeURL(state, nonce)
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (h *AuthHandler) GoogleCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing state or code")
		return
	}

	cookie, err := r.Cookie(auth.OIDCStateCookieName)
	if err != nil {
		writeError(w, http.StatusBadRequest, "state_missing", "missing login state")
		return
	}

	var saved oidcState
	if err := h.sessions.Decode(auth.OIDCStateCookieName, cookie.Value, &saved); err != nil {
		writeError(w, http.StatusBadRequest, "state_invalid", "invalid login state")
		return
	}
	if saved.State != state {
		writeError(w, http.StatusBadRequest, "state_mismatch", "state mismatch")
		return
	}

	claims, err := h.oidc.Exchange(r.Context(), code, saved.Nonce)
	if err != nil {
		h.logger.Error("oidc exchange failed", "error", err)
		writeError(w, http.StatusUnauthorized, "oidc_exchange_failed", "authentication failed")
		return
	}

	user, err := store.FindOrCreateGoogleUser(r.Context(), h.pool, claims.Sub, claims.Email, claims.Name, h.autoProvision)
	if err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			writeError(w, http.StatusUnauthorized, "user_not_found", "user not allowed")
			return
		}
		if errors.Is(err, store.ErrUserDisabled) {
			writeError(w, http.StatusForbidden, "user_disabled", "user disabled")
			return
		}
		h.logger.Error("failed to resolve user", "error", err)
		writeError(w, http.StatusInternalServerError, "user_lookup_failed", "user resolution failed")
		return
	}

	csrfToken, err := auth.RandomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "csrf_error", "failed to issue session")
		return
	}

	if err := h.sessions.SetSession(w, auth.Session{
		UserID:    user.ID,
		Role:      user.Role,
		Email:     user.Email,
		CSRFToken: csrfToken,
		ProjectID: nil,
	}); err != nil {
		h.logger.Error("set session failed", "error", err)
		writeError(w, http.StatusInternalServerError, "session_error", "failed to create session")
		return
	}

	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "login_success",
		EntityType: "user",
		EntityID:   &user.ID,
		Payload: map[string]any{
			"email": user.Email,
			"ts":    time.Now().UTC(),
		},
	})

	http.Redirect(w, r, "/ui", http.StatusFound)
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	h.sessions.ClearSession(w)

	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "logout",
		EntityType: "user",
		EntityID:   &user.ID,
		Payload: map[string]any{
			"email": user.Email,
			"ts":    time.Now().UTC(),
		},
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
