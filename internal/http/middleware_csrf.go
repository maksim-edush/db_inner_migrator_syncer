package httpserver

import (
	"net/http"
	"strings"

	"db_inner_migrator_syncer/internal/auth"
)

func CSRFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requiresCSRF(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		user, ok := auth.UserFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		token := r.Header.Get("X-CSRF-Token")
		if token == "" {
			token = r.PostFormValue("csrf_token")
		}
		if token == "" || token != user.CSRFToken {
			writeError(w, http.StatusForbidden, "csrf_invalid", "invalid csrf token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requiresCSRF(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}
