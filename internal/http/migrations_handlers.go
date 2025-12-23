package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"db_inner_migrator_syncer/internal/audit"
	"db_inner_migrator_syncer/internal/auth"
	"db_inner_migrator_syncer/internal/store"
)

type MigrationHandler struct {
	pool   *pgxpool.Pool
	logger requestLogger
}

func NewMigrationHandler(pool *pgxpool.Pool, logger requestLogger) *MigrationHandler {
	return &MigrationHandler{
		pool:   pool,
		logger: logger,
	}
}

type createMigrationRequest struct {
	Key             string  `json:"key"`
	Name            string  `json:"name"`
	Jira            string  `json:"jira"`
	Description     string  `json:"description"`
	SQLUp           string  `json:"sql_up"`
	SQLDown         *string `json:"sql_down"`
	TransactionMode string  `json:"transaction_mode"`
}

type updateMigrationRequest struct {
	Name            *string `json:"name"`
	Jira            *string `json:"jira"`
	Description     *string `json:"description"`
	SQLUp           *string `json:"sql_up"`
	SQLDown         *string `json:"sql_down"`
	TransactionMode *string `json:"transaction_mode"`
}

func (h *MigrationHandler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	projectID, ok := requireProject(w, user)
	if !ok {
		return
	}
	q := r.URL.Query().Get("q")
	items, err := store.ListMigrations(r.Context(), h.pool, projectID, q)
	if err != nil {
		h.logger.Error("list migrations failed", "error", err)
		writeError(w, http.StatusInternalServerError, "list_failed", "failed to list migrations")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"migrations": items})
}

func (h *MigrationHandler) Get(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	projectID, ok := requireProject(w, user)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid migration id")
		return
	}
	m, err := store.GetMigration(r.Context(), h.pool, projectID, id)
	if err != nil {
		if errors.Is(err, store.ErrMigrationNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "migration not found")
			return
		}
		h.logger.Error("get migration failed", "error", err)
		writeError(w, http.StatusInternalServerError, "lookup_failed", "failed to fetch migration")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (h *MigrationHandler) Create(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	projectID, ok := requireProject(w, user)
	if !ok {
		return
	}
	var req createMigrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid json body")
		return
	}

	m, err := store.CreateMigration(r.Context(), h.pool, store.CreateMigrationInput{
		ProjectID:       projectID,
		Key:             req.Key,
		Name:            req.Name,
		Jira:            req.Jira,
		Description:     req.Description,
		SQLUp:           req.SQLUp,
		SQLDown:         req.SQLDown,
		TransactionMode: req.TransactionMode,
		CreatedBy:       user.ID,
	})
	if err != nil {
		if errors.Is(err, store.ErrMigrationKeyEmpty) ||
			errors.Is(err, store.ErrMigrationNameEmpty) ||
			errors.Is(err, store.ErrMigrationSQLMissing) ||
			errors.Is(err, store.ErrTxModeInvalid) {
			writeError(w, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
		if err.Error() == "migration key already exists" {
			writeError(w, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
		h.logger.Error("create migration failed", "error", err)
		writeError(w, http.StatusInternalServerError, "create_failed", "failed to create migration")
		return
	}

	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "migration_created",
		EntityType: "migration",
		EntityID:   &m.ID,
		Payload: map[string]any{
			"key":     m.Key,
			"version": m.Version,
		},
	})

	writeJSON(w, http.StatusCreated, m)
}

func (h *MigrationHandler) Update(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	projectID, ok := requireProject(w, user)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid migration id")
		return
	}
	var req updateMigrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid json body")
		return
	}

	m, sqlChanged, err := store.UpdateMigration(r.Context(), h.pool, projectID, id, store.UpdateMigrationInput{
		Name:            req.Name,
		Jira:            req.Jira,
		Description:     req.Description,
		SQLUp:           req.SQLUp,
		SQLDown:         req.SQLDown,
		TransactionMode: req.TransactionMode,
	})
	if err != nil {
		if errors.Is(err, store.ErrMigrationNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "migration not found")
			return
		}
		if errors.Is(err, store.ErrMigrationNameEmpty) ||
			errors.Is(err, store.ErrMigrationSQLMissing) || errors.Is(err, store.ErrTxModeInvalid) {
			writeError(w, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
		h.logger.Error("update migration failed", "error", err)
		writeError(w, http.StatusInternalServerError, "update_failed", "failed to update migration")
		return
	}

	if sqlChanged {
		if err := store.DeleteApprovalsForMigration(r.Context(), h.pool, m.ID); err != nil {
			h.logger.Error("delete approvals on edit failed", "error", err)
		} else {
			_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
				ActorID:    &user.ID,
				Action:     "migration_approvals_invalidated",
				EntityType: "migration",
				EntityID:   &m.ID,
				Payload: map[string]any{
					"version": m.Version,
				},
			})
		}
	}

	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "migration_updated",
		EntityType: "migration",
		EntityID:   &m.ID,
		Payload: map[string]any{
			"version":     m.Version,
			"sql_changed": sqlChanged,
		},
	})

	writeJSON(w, http.StatusOK, m)
}
