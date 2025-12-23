package httpserver

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type HealthHandler struct {
	DB *pgxpool.Pool
}

type healthResponse struct {
	Status string `json:"status"`
	DB     string `json:"db"`
}

func (h HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	dbStatus := "ok"
	if err := h.DB.Ping(ctx); err != nil {
		dbStatus = "unhealthy"
		writeError(w, http.StatusServiceUnavailable, "service_unhealthy", "database unreachable")
		return
	}

	writeJSON(w, http.StatusOK, healthResponse{
		Status: "ok",
		DB:     dbStatus,
	})
}
