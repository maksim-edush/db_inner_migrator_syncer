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

type ProjectHandler struct {
	pool     *pgxpool.Pool
	logger   requestLogger
	sessions *auth.SessionManager
}

func NewProjectHandler(pool *pgxpool.Pool, logger requestLogger, sessions *auth.SessionManager) *ProjectHandler {
	return &ProjectHandler{
		pool:     pool,
		logger:   logger,
		sessions: sessions,
	}
}

func (h *ProjectHandler) List(w http.ResponseWriter, r *http.Request) {
	projects, err := store.ListProjects(r.Context(), h.pool)
	if err != nil {
		h.logger.Error("list projects failed", "error", err)
		writeError(w, http.StatusInternalServerError, "list_failed", "failed to list projects")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

type createProjectRequest struct {
	Name string `json:"name"`
}

func (h *ProjectHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid json body")
		return
	}

	project, err := store.CreateProject(r.Context(), h.pool, req.Name)
	if err != nil {
		if errors.Is(err, store.ErrProjectNameEmpty) || errors.Is(err, store.ErrProjectNameExists) {
			writeError(w, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
		h.logger.Error("create project failed", "error", err)
		writeError(w, http.StatusInternalServerError, "create_failed", "failed to create project")
		return
	}

	user, _ := auth.UserFromContext(r.Context())
	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "project_created",
		EntityType: "project",
		EntityID:   &project.ID,
		Payload: map[string]any{
			"name": project.Name,
		},
	})

	writeJSON(w, http.StatusCreated, project)
}

func (h *ProjectHandler) Select(w http.ResponseWriter, r *http.Request) {
	projectIDStr := chi.URLParam(r, "id")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_project_id", "invalid project id")
		return
	}

	project, err := store.GetProject(r.Context(), h.pool, projectID)
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "project not found")
			return
		}
		h.logger.Error("get project failed", "error", err)
		writeError(w, http.StatusInternalServerError, "select_failed", "failed to select project")
		return
	}

	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	// persist selection in session cookie
	newSession := auth.Session{
		UserID:    user.ID,
		Role:      user.Role,
		Email:     user.Email,
		CSRFToken: user.CSRFToken,
		ProjectID: &project.ID,
	}
	if err := h.sessions.SetSession(w, newSession); err != nil {
		h.logger.Error("set session project failed", "error", err)
		writeError(w, http.StatusInternalServerError, "session_error", "failed to update session")
		return
	}

	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "project_selected",
		EntityType: "project",
		EntityID:   &project.ID,
		Payload: map[string]any{
			"name": project.Name,
		},
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"project": project,
	})
}
