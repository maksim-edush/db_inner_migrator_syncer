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

type DBInventoryHandler struct {
	pool      *pgxpool.Pool
	logger    requestLogger
	sessions  *auth.SessionManager
	secretKey []byte
}

func NewDBInventoryHandler(pool *pgxpool.Pool, logger requestLogger, sessions *auth.SessionManager, secretKey []byte) *DBInventoryHandler {
	return &DBInventoryHandler{
		pool:      pool,
		logger:    logger,
		sessions:  sessions,
		secretKey: secretKey,
	}
}

func (h *DBInventoryHandler) ListDBSets(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	projectID, ok := requireProject(w, user)
	if !ok {
		return
	}

	env := r.URL.Query().Get("env")
	sets, err := store.ListDBSets(r.Context(), h.pool, projectID, env)
	if err != nil {
		h.logger.Error("list db sets failed", "error", err)
		writeError(w, http.StatusInternalServerError, "list_failed", "failed to list db sets")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"db_sets": sets})
}

type createDBSetRequest struct {
	Env  string `json:"env"`
	Name string `json:"name"`
}

func (h *DBInventoryHandler) CreateDBSet(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	projectID, ok := requireProject(w, user)
	if !ok {
		return
	}

	var req createDBSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid json body")
		return
	}

	set, err := store.CreateDBSet(r.Context(), h.pool, projectID, req.Env, req.Name, user.ID)
	if err != nil {
		if errors.Is(err, store.ErrEnvInvalid) || errors.Is(err, store.ErrDBSetNameEmpty) {
			writeError(w, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
		h.logger.Error("create db set failed", "error", err)
		writeError(w, http.StatusInternalServerError, "create_failed", "failed to create db set")
		return
	}

	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "db_set_created",
		EntityType: "db_set",
		EntityID:   &set.ID,
		Payload: map[string]any{
			"name": set.Name,
			"env":  set.Env,
		},
	})

	writeJSON(w, http.StatusCreated, set)
}

func (h *DBInventoryHandler) DisableDBSet(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	projectID, ok := requireProject(w, user)
	if !ok {
		return
	}

	setID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid db set id")
		return
	}
	set, err := store.GetDBSet(r.Context(), h.pool, setID)
	if err != nil {
		if errors.Is(err, store.ErrDBSetNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "db set not found")
			return
		}
		h.logger.Error("get db set failed", "error", err)
		writeError(w, http.StatusInternalServerError, "lookup_failed", "failed to fetch db set")
		return
	}
	if set.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "not_found", "db set not found")
		return
	}

	if err := store.DisableDBSet(r.Context(), h.pool, setID); err != nil {
		if errors.Is(err, store.ErrDBSetNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "db set not found")
			return
		}
		h.logger.Error("disable db set failed", "error", err)
		writeError(w, http.StatusInternalServerError, "disable_failed", "failed to disable db set")
		return
	}

	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "db_set_disabled",
		EntityType: "db_set",
		EntityID:   &set.ID,
		Payload: map[string]any{
			"name": set.Name,
			"env":  set.Env,
		},
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type createTargetRequest struct {
	Engine   string         `json:"engine"`
	Host     string         `json:"host"`
	Port     int            `json:"port"`
	DBName   string         `json:"dbname"`
	Username string         `json:"username"`
	Password string         `json:"password"`
	Options  map[string]any `json:"options"`
}

func (h *DBInventoryHandler) ListTargets(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	projectID, ok := requireProject(w, user)
	if !ok {
		return
	}
	setID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid db set id")
		return
	}
	set, err := store.GetDBSet(r.Context(), h.pool, setID)
	if err != nil {
		if errors.Is(err, store.ErrDBSetNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "db set not found")
			return
		}
		h.logger.Error("get db set failed", "error", err)
		writeError(w, http.StatusInternalServerError, "lookup_failed", "failed to fetch db set")
		return
	}
	if set.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "not_found", "db set not found")
		return
	}

	targets, err := store.ListDBTargetsBySet(r.Context(), h.pool, setID)
	if err != nil {
		h.logger.Error("list targets failed", "error", err)
		writeError(w, http.StatusInternalServerError, "list_failed", "failed to list db targets")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"db_targets": targets})
}

func (h *DBInventoryHandler) CreateTarget(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	projectID, ok := requireProject(w, user)
	if !ok {
		return
	}

	setID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid db set id")
		return
	}
	set, err := store.GetDBSet(r.Context(), h.pool, setID)
	if err != nil {
		if errors.Is(err, store.ErrDBSetNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "db set not found")
			return
		}
		h.logger.Error("get db set failed", "error", err)
		writeError(w, http.StatusInternalServerError, "lookup_failed", "failed to fetch db set")
		return
	}
	if set.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "not_found", "db set not found")
		return
	}

	var req createTargetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid json body")
		return
	}

	target, err := store.CreateDBTarget(r.Context(), h.pool, h.secretKey, store.CreateTargetInput{
		DBSetID:  setID,
		Engine:   req.Engine,
		Host:     req.Host,
		Port:     req.Port,
		DBName:   req.DBName,
		Username: req.Username,
		Password: req.Password,
		Options:  req.Options,
	})
	if err != nil {
		if errors.Is(err, store.ErrDBTargetBadEngine) || errors.Is(err, store.ErrDBTargetInactive) {
			writeError(w, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
		if err.Error() == "host, dbname, username required" || err.Error() == "port must be positive" {
			writeError(w, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
		h.logger.Error("create target failed", "error", err)
		writeError(w, http.StatusInternalServerError, "create_failed", "failed to create db target")
		return
	}

	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "db_target_created",
		EntityType: "db_target",
		EntityID:   &target.ID,
		Payload: map[string]any{
			"db_set_id": setID,
			"engine":    target.Engine,
			"host":      target.Host,
			"port":      target.Port,
		},
	})

	writeJSON(w, http.StatusCreated, target)
}

func (h *DBInventoryHandler) GetTarget(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	projectID, ok := requireProject(w, user)
	if !ok {
		return
	}
	targetID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid target id")
		return
	}
	target, _, err := store.GetDBTarget(r.Context(), h.pool, targetID)
	if err != nil {
		if errors.Is(err, store.ErrDBTargetNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "db target not found")
			return
		}
		h.logger.Error("get target failed", "error", err)
		writeError(w, http.StatusInternalServerError, "lookup_failed", "failed to fetch db target")
		return
	}
	set, err := store.GetDBSet(r.Context(), h.pool, target.DBSetID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "db set not found")
		return
	}
	if set.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "not_found", "db target not found")
		return
	}

	writeJSON(w, http.StatusOK, target)
}

func (h *DBInventoryHandler) DisableTarget(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	projectID, ok := requireProject(w, user)
	if !ok {
		return
	}
	targetID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid target id")
		return
	}
	target, _, err := store.GetDBTarget(r.Context(), h.pool, targetID)
	if err != nil {
		if errors.Is(err, store.ErrDBTargetNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "db target not found")
			return
		}
		h.logger.Error("get target failed", "error", err)
		writeError(w, http.StatusInternalServerError, "lookup_failed", "failed to fetch db target")
		return
	}
	set, err := store.GetDBSet(r.Context(), h.pool, target.DBSetID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "db set not found")
		return
	}
	if set.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "not_found", "db target not found")
		return
	}

	if err := store.DisableDBTarget(r.Context(), h.pool, targetID); err != nil {
		if errors.Is(err, store.ErrDBTargetNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "db target not found")
			return
		}
		h.logger.Error("disable target failed", "error", err)
		writeError(w, http.StatusInternalServerError, "disable_failed", "failed to disable db target")
		return
	}

	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "db_target_disabled",
		EntityType: "db_target",
		EntityID:   &target.ID,
		Payload: map[string]any{
			"db_set_id": target.DBSetID,
			"engine":    target.Engine,
		},
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *DBInventoryHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	projectID, ok := requireProject(w, user)
	if !ok {
		return
	}
	targetID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid target id")
		return
	}
	target, _, err := store.GetDBTarget(r.Context(), h.pool, targetID)
	if err != nil {
		if errors.Is(err, store.ErrDBTargetNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "db target not found")
			return
		}
		h.logger.Error("get target failed", "error", err)
		writeError(w, http.StatusInternalServerError, "lookup_failed", "failed to fetch db target")
		return
	}
	set, err := store.GetDBSet(r.Context(), h.pool, target.DBSetID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "db set not found")
		return
	}
	if set.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "not_found", "db target not found")
		return
	}

	err = store.TestTargetConnection(r.Context(), h.pool, h.secretKey, targetID)
	if err != nil {
		h.logger.Error("test target connection failed", "error", err)
		_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
			ActorID:    &user.ID,
			Action:     "db_target_test_failed",
			EntityType: "db_target",
			EntityID:   &target.ID,
			Payload: map[string]any{
				"db_set_id": target.DBSetID,
				"engine":    target.Engine,
				"error":     err.Error(),
			},
		})
		writeError(w, http.StatusBadRequest, "connection_failed", "failed to connect to target")
		return
	}

	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "db_target_test_success",
		EntityType: "db_target",
		EntityID:   &target.ID,
		Payload: map[string]any{
			"db_set_id": target.DBSetID,
			"engine":    target.Engine,
		},
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func requireProject(w http.ResponseWriter, user *auth.User) (uuid.UUID, bool) {
	if user == nil || user.ProjectID == nil {
		writeError(w, http.StatusBadRequest, "project_required", "select a project first")
		return uuid.UUID{}, false
	}
	return *user.ProjectID, true
}
