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
	"db_inner_migrator_syncer/internal/executor"
	"db_inner_migrator_syncer/internal/store"
)

type RunHandler struct {
	pool     *pgxpool.Pool
	logger   requestLogger
	executor *executor.Executor
}

func NewRunHandler(pool *pgxpool.Pool, logger requestLogger, executor *executor.Executor) *RunHandler {
	return &RunHandler{pool: pool, logger: logger, executor: executor}
}

type requestApprovalRequest struct {
	Env     string `json:"env"`
	DBSetID string `json:"db_set_id"`
}

type decisionRequest struct {
	Comment string `json:"comment"`
}

func (h *RunHandler) Get(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	projectID, ok := requireProject(w, user)
	if !ok {
		return
	}
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid run id")
		return
	}
	run, err := store.GetRunWithItems(r.Context(), h.pool, projectID, runID)
	if err != nil {
		if errors.Is(err, store.ErrRunNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "run not found")
			return
		}
		h.logger.Error("get run failed", "error", err)
		writeError(w, http.StatusInternalServerError, "lookup_failed", "failed to fetch run")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *RunHandler) ListForMigration(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	projectID, ok := requireProject(w, user)
	if !ok {
		return
	}
	migrationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid migration id")
		return
	}
	runs, err := store.ListRunsForMigration(r.Context(), h.pool, projectID, migrationID)
	if err != nil {
		h.logger.Error("list runs failed", "error", err)
		writeError(w, http.StatusInternalServerError, "list_failed", "failed to list runs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (h *RunHandler) RequestApproval(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	projectID, ok := requireProject(w, user)
	if !ok {
		return
	}
	migrationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid migration id")
		return
	}
	var req requestApprovalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid json body")
		return
	}
	dbSetID, err := uuid.Parse(req.DBSetID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_db_set_id", "invalid db set id")
		return
	}

	run, err := store.RequestRun(r.Context(), h.pool, store.RequestRunInput{
		ProjectID:   projectID,
		MigrationID: migrationID,
		DBSetID:     dbSetID,
		Env:         req.Env,
		RequestedBy: user.ID,
		RunType:     "apply",
	})
	if err != nil {
		if errors.Is(err, store.ErrRunEnvInvalid) || errors.Is(err, store.ErrRunNoTargets) {
			writeError(w, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
		if errors.Is(err, store.ErrDBSetNotFound) || errors.Is(err, store.ErrMigrationNotFound) {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		h.logger.Error("request run failed", "error", err)
		writeError(w, http.StatusInternalServerError, "request_failed", "failed to request approval")
		return
	}

	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "run_requested",
		EntityType: "run",
		EntityID:   &run.ID,
		Payload: map[string]any{
			"migration_id": run.MigrationID,
			"env":          run.Env,
			"db_set_id":    run.DBSetID,
		},
	})

	writeJSON(w, http.StatusCreated, run)
}

func (h *RunHandler) RequestRollback(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	projectID, ok := requireProject(w, user)
	if !ok {
		return
	}
	migrationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid migration id")
		return
	}
	var req requestApprovalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid json body")
		return
	}
	dbSetID, err := uuid.Parse(req.DBSetID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_db_set_id", "invalid db set id")
		return
	}

	run, err := store.RequestRun(r.Context(), h.pool, store.RequestRunInput{
		ProjectID:   projectID,
		MigrationID: migrationID,
		DBSetID:     dbSetID,
		Env:         req.Env,
		RequestedBy: user.ID,
		RunType:     "rollback",
	})
	if err != nil {
		if errors.Is(err, store.ErrRunEnvInvalid) || errors.Is(err, store.ErrRunNoTargets) || errors.Is(err, store.ErrRollbackMissingSQL) {
			writeError(w, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
		if errors.Is(err, store.ErrDBSetNotFound) || errors.Is(err, store.ErrMigrationNotFound) {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		h.logger.Error("request rollback failed", "error", err)
		writeError(w, http.StatusInternalServerError, "request_failed", "failed to request rollback")
		return
	}

	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "rollback_requested",
		EntityType: "run",
		EntityID:   &run.ID,
		Payload: map[string]any{
			"migration_id": run.MigrationID,
			"env":          run.Env,
			"db_set_id":    run.DBSetID,
		},
	})

	writeJSON(w, http.StatusCreated, run)
}

func (h *RunHandler) Approve(w http.ResponseWriter, r *http.Request) {
	h.handleDecision(w, r, "approved")
}

func (h *RunHandler) Deny(w http.ResponseWriter, r *http.Request) {
	h.handleDecision(w, r, "denied")
}

func (h *RunHandler) Execute(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	projectID, ok := requireProject(w, user)
	if !ok {
		return
	}
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid run id")
		return
	}

	run, err := h.executor.ExecuteRun(r.Context(), projectID, runID, user.ID)
	if err != nil {
		if errors.Is(err, store.ErrRunNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "run not found")
			return
		}
		if errors.Is(err, store.ErrChecksumMismatch) {
			writeError(w, http.StatusConflict, "checksum_mismatch", err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, "execution_failed", err.Error())
		_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
			ActorID:    &user.ID,
			Action:     "run_execute_failed",
			EntityType: "run",
			EntityID:   &runID,
			Payload: map[string]any{
				"error": err.Error(),
			},
		})
		return
	}

	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "run_executed",
		EntityType: "run",
		EntityID:   &run.ID,
		Payload: map[string]any{
			"status": run.Status,
		},
	})

	writeJSON(w, http.StatusOK, run)
}

func (h *RunHandler) handleDecision(w http.ResponseWriter, r *http.Request, decision string) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	projectID, ok := requireProject(w, user)
	if !ok {
		return
	}
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid run id")
		return
	}

	var req decisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid json body")
		return
	}

	input := store.ApprovalDecisionInput{
		RunID:     runID,
		ProjectID: projectID,
		ActorID:   user.ID,
		Comment:   req.Comment,
		Decision:  decision,
	}

	var run *store.Run
	switch decision {
	case "approved":
		run, err = store.ApproveRun(r.Context(), h.pool, input)
	case "denied":
		run, err = store.DenyRun(r.Context(), h.pool, input)
	default:
		writeError(w, http.StatusBadRequest, "invalid_decision", "invalid decision")
		return
	}
	if err != nil {
		if errors.Is(err, store.ErrRunNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "run not found")
			return
		}
		if errors.Is(err, store.ErrRunInvalidStatus) {
			writeError(w, http.StatusBadRequest, "invalid_status", "run not awaiting approval")
			return
		}
		if errors.Is(err, store.ErrChecksumMismatch) {
			writeError(w, http.StatusConflict, "checksum_mismatch", err.Error())
			return
		}
		h.logger.Error("run decision failed", "error", err)
		writeError(w, http.StatusInternalServerError, "decision_failed", "failed to process decision")
		return
	}

	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "run_" + decision,
		EntityType: "run",
		EntityID:   &run.ID,
		Payload: map[string]any{
			"migration_id": run.MigrationID,
			"env":          run.Env,
			"db_set_id":    run.DBSetID,
			"comment":      req.Comment,
		},
	})

	writeJSON(w, http.StatusOK, run)
}
