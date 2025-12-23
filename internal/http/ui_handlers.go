package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"db_inner_migrator_syncer/internal/audit"
	"db_inner_migrator_syncer/internal/auth"
	"db_inner_migrator_syncer/internal/executor"
	"db_inner_migrator_syncer/internal/rbac"
	"db_inner_migrator_syncer/internal/store"
)

type UIHandler struct {
	pool          *pgxpool.Pool
	logger        requestLogger
	sessions      *auth.SessionManager
	authenticator auth.Authenticator
	renderer      *TemplateRenderer
	secretKey     []byte
	executor      *executor.Executor
}

func NewUIHandler(pool *pgxpool.Pool, logger requestLogger, sessions *auth.SessionManager, authenticator auth.Authenticator, renderer *TemplateRenderer, secretKey []byte, exec *executor.Executor) *UIHandler {
	return &UIHandler{
		pool:          pool,
		logger:        logger,
		sessions:      sessions,
		authenticator: authenticator,
		renderer:      renderer,
		secretKey:     secretKey,
		executor:      exec,
	}
}

func (h *UIHandler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := h.authenticator.Authenticate(r)
		if err != nil || user == nil {
			http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
			return
		}
		ctx := auth.WithUser(r.Context(), user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *UIHandler) Login(w http.ResponseWriter, r *http.Request) {
	user, err := h.authenticator.Authenticate(r)
	if err == nil && user != nil {
		http.Redirect(w, r, "/ui", http.StatusSeeOther)
		return
	}
	data := UIData{
		Title:    "migrate-hub",
		Template: "login",
		Path:     r.URL.Path,
	}
	h.renderer.Render(w, data)
}

func (h *UIHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	data, session := h.baseData(w, r)
	if user == nil || session == nil {
		return
	}
	if user.ProjectID == nil {
		data.Page = dashboardPage{NeedsProject: true}
		h.renderer.Render(w, data)
		return
	}

	projectID := *user.ProjectID
	pending, _ := store.CountRunsByStatus(r.Context(), h.pool, projectID, "awaiting_approval")
	running, _ := store.CountRunsByStatus(r.Context(), h.pool, projectID, "running")
	failed, _ := store.CountFailedRunsSince(r.Context(), h.pool, projectID, time.Now().Add(-24*time.Hour))
	recent, _ := store.ListRecentRuns(r.Context(), h.pool, projectID, 20)

	data.Page = dashboardPage{
		PendingCount: pending,
		RunningCount: running,
		FailedCount:  failed,
		RecentRuns:   recent,
	}
	h.renderer.Render(w, data)
}

func (h *UIHandler) Projects(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	data, _ := h.baseData(w, r)
	if user == nil {
		return
	}

	isAdmin := user.Role == rbac.RoleAdmin
	inventories, err := h.projectInventories(r.Context(), data.Projects)
	if err != nil {
		h.renderError(w, r, http.StatusInternalServerError, "Failed to load project inventory.")
		return
	}
	data.Page = projectsPage{
		IsAdmin:     isAdmin,
		Inventories: inventories,
	}
	h.renderer.Render(w, data)
}

func (h *UIHandler) Users(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	data, _ := h.baseData(w, r)
	if user == nil {
		return
	}
	if user.Role != rbac.RoleAdmin {
		h.renderError(w, r, http.StatusForbidden, "Admin role required.")
		return
	}
	users, err := store.ListUsers(r.Context(), h.pool)
	if err != nil {
		h.renderError(w, r, http.StatusInternalServerError, "Failed to list users.")
		return
	}
	data.Page = usersPage{Users: users}
	h.renderer.Render(w, data)
}

func (h *UIHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	if user == nil {
		return
	}
	if user.Role != rbac.RoleAdmin {
		h.renderError(w, r, http.StatusForbidden, "Admin role required.")
		return
	}
	input := store.CreateUserInput{
		Email: r.FormValue("email"),
		Name:  r.FormValue("name"),
		Role:  rbac.Role(r.FormValue("role")),
	}
	created, err := store.CreateUser(r.Context(), h.pool, input)
	if err != nil {
		h.setFlash(w, r, "error", err.Error())
		http.Redirect(w, r, "/ui/users", http.StatusSeeOther)
		return
	}
	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "user_created",
		EntityType: "user",
		EntityID:   &created.ID,
		Payload: map[string]any{
			"email": created.Email,
			"role":  created.Role,
			"name":  created.Name,
		},
	})
	h.setFlash(w, r, "success", "User created.")
	http.Redirect(w, r, "/ui/users", http.StatusSeeOther)
}

func (h *UIHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	if user == nil {
		return
	}
	if user.Role != rbac.RoleAdmin {
		h.renderError(w, r, http.StatusForbidden, "Admin role required.")
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.setFlash(w, r, "error", "Invalid user id.")
		http.Redirect(w, r, "/ui/users", http.StatusSeeOther)
		return
	}
	input := store.UpdateUserInput{
		Name: r.FormValue("name"),
		Role: rbac.Role(r.FormValue("role")),
	}
	updated, err := store.UpdateUser(r.Context(), h.pool, userID, input)
	if err != nil {
		h.setFlash(w, r, "error", err.Error())
		http.Redirect(w, r, "/ui/users", http.StatusSeeOther)
		return
	}
	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "user_updated",
		EntityType: "user",
		EntityID:   &updated.ID,
		Payload: map[string]any{
			"email": updated.Email,
			"role":  updated.Role,
			"name":  updated.Name,
		},
	})
	h.setFlash(w, r, "success", "User updated.")
	http.Redirect(w, r, "/ui/users", http.StatusSeeOther)
}

func (h *UIHandler) DisableUser(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	if user == nil {
		return
	}
	if user.Role != rbac.RoleAdmin {
		h.renderError(w, r, http.StatusForbidden, "Admin role required.")
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.setFlash(w, r, "error", "Invalid user id.")
		http.Redirect(w, r, "/ui/users", http.StatusSeeOther)
		return
	}
	target, err := store.GetUserRecordByID(r.Context(), h.pool, userID)
	if err != nil {
		h.setFlash(w, r, "error", err.Error())
		http.Redirect(w, r, "/ui/users", http.StatusSeeOther)
		return
	}
	if err := store.DisableUser(r.Context(), h.pool, userID); err != nil {
		h.setFlash(w, r, "error", err.Error())
		http.Redirect(w, r, "/ui/users", http.StatusSeeOther)
		return
	}
	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "user_disabled",
		EntityType: "user",
		EntityID:   &userID,
		Payload: map[string]any{
			"email": target.Email,
		},
	})
	h.setFlash(w, r, "success", "User disabled.")
	http.Redirect(w, r, "/ui/users", http.StatusSeeOther)
}

func (h *UIHandler) CreateProject(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	if user == nil {
		return
	}
	if user.Role != rbac.RoleAdmin {
		h.renderError(w, r, http.StatusForbidden, "Admin role required.")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	project, err := store.CreateProject(r.Context(), h.pool, name)
	if err != nil {
		h.setFlash(w, r, "error", err.Error())
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "project_created",
		EntityType: "project",
		EntityID:   &project.ID,
		Payload: map[string]any{
			"name": project.Name,
		},
	})
	h.setFlash(w, r, "success", "Project created.")
	http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
}

func (h *UIHandler) SelectProject(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	if user == nil {
		return
	}
	projectIDStr := r.FormValue("project_id")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		h.setFlash(w, r, "error", "Invalid project id.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	project, err := store.GetProject(r.Context(), h.pool, projectID)
	if err != nil {
		h.setFlash(w, r, "error", "Project not found.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	session, err := h.sessions.GetSession(r)
	if err != nil {
		h.renderError(w, r, http.StatusUnauthorized, "Session invalid.")
		return
	}
	session.ProjectID = &projectID
	if err := h.sessions.SetSession(w, *session); err != nil {
		h.renderError(w, r, http.StatusInternalServerError, "Failed to update session.")
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
	http.Redirect(w, r, r.Referer(), http.StatusSeeOther)
}

func (h *UIHandler) DBSetList(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	data, _ := h.baseData(w, r)
	if user == nil {
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	env := r.URL.Query().Get("env")
	if env == "" {
		env = "stg"
	}
	sets, err := store.ListDBSets(r.Context(), h.pool, *user.ProjectID, env)
	if err != nil {
		h.renderError(w, r, http.StatusInternalServerError, "Failed to list db sets.")
		return
	}
	data.Page = dbSetsPage{
		Env:     env,
		DBSets:  sets,
		IsAdmin: user.Role == rbac.RoleAdmin,
	}
	h.renderer.Render(w, data)
}

func (h *UIHandler) CreateDBSet(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	if user == nil {
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	env := r.FormValue("env")
	name := r.FormValue("name")
	set, err := store.CreateDBSet(r.Context(), h.pool, *user.ProjectID, env, name, user.ID)
	if err != nil {
		h.setFlash(w, r, "error", err.Error())
		http.Redirect(w, r, "/ui/db-sets?env="+url.QueryEscape(env), http.StatusSeeOther)
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
	h.setFlash(w, r, "success", "DB set created.")
	http.Redirect(w, r, "/ui/db-sets?env="+url.QueryEscape(env), http.StatusSeeOther)
}

func (h *UIHandler) DBSetDetail(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	data, _ := h.baseData(w, r)
	if user == nil {
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	setID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid db set id.")
		return
	}
	set, err := store.GetDBSet(r.Context(), h.pool, setID)
	if err != nil || set.ProjectID != *user.ProjectID {
		h.renderError(w, r, http.StatusNotFound, "DB set not found.")
		return
	}
	targets, err := store.ListDBTargetsBySet(r.Context(), h.pool, set.ID)
	if err != nil {
		h.renderError(w, r, http.StatusInternalServerError, "Failed to list targets.")
		return
	}
	data.Page = dbSetDetailPage{
		DBSet:   *set,
		Targets: targets,
		IsAdmin: user.Role == rbac.RoleAdmin,
	}
	h.renderer.Render(w, data)
}

func (h *UIHandler) TargetMigrations(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	data, _ := h.baseData(w, r)
	if user == nil {
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}

	env := strings.TrimSpace(r.URL.Query().Get("env"))
	if env == "" {
		env = "stg"
	}
	envFilter := env
	if env == "all" {
		envFilter = ""
	}
	onlyMissing := r.URL.Query().Get("only_missing") == "1"

	migs, err := store.ListMigrations(r.Context(), h.pool, *user.ProjectID, "")
	if err != nil {
		h.renderError(w, r, http.StatusInternalServerError, "Failed to load migrations.")
		return
	}
	sets, err := store.ListDBSets(r.Context(), h.pool, *user.ProjectID, envFilter)
	if err != nil {
		h.renderError(w, r, http.StatusInternalServerError, "Failed to load db sets.")
		return
	}
	latestByTarget, err := store.ListLatestRunItemsByTargetMigration(r.Context(), h.pool, *user.ProjectID, envFilter)
	if err != nil {
		h.renderError(w, r, http.StatusInternalServerError, "Failed to load run history.")
		return
	}

	var targets []targetMigrationView
	for _, set := range sets {
		tgts, err := store.ListDBTargetsBySet(r.Context(), h.pool, set.ID)
		if err != nil {
			h.renderError(w, r, http.StatusInternalServerError, "Failed to load db targets.")
			return
		}
		for _, target := range tgts {
			view := targetMigrationView{
				DBSet:     set,
				Target:    target,
				MaskedDSN: maskedDSN(target),
			}
			for _, mig := range migs {
				status := compareStatus{Label: "missing"}
				if byMig, ok := latestByTarget[target.ID]; ok {
					if item, ok := byMig[mig.ID]; ok {
						status = compareStatusFromLatest(item)
					}
				}
				switch status.Label {
				case "applied":
					view.AppliedCount++
				case "missing":
					view.MissingCount++
				case "rollbacked":
					view.RollbackedCount++
				case "failed":
					view.FailedCount++
				case "approved":
					view.ApprovedCount++
				case "awaiting_approval":
					view.AwaitingCount++
				case "running":
					view.RunningCount++
				}
				if onlyMissing && status.Label != "missing" {
					continue
				}
				view.Rows = append(view.Rows, targetMigrationRow{
					Migration: mig,
					Status:    status,
				})
			}
			targets = append(targets, view)
		}
	}

	data.Page = targetsPage{
		Env:         env,
		OnlyMissing: onlyMissing,
		Targets:     targets,
	}
	h.renderer.Render(w, data)
}

func (h *UIHandler) AddTarget(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	if user == nil {
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	setID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.setFlash(w, r, "error", "Invalid db set id.")
		http.Redirect(w, r, "/ui/db-sets", http.StatusSeeOther)
		return
	}
	set, err := store.GetDBSet(r.Context(), h.pool, setID)
	if err != nil || set.ProjectID != *user.ProjectID {
		h.renderError(w, r, http.StatusNotFound, "DB set not found.")
		return
	}

	port, _ := strconv.Atoi(r.FormValue("port"))
	var options map[string]any
	if raw := strings.TrimSpace(r.FormValue("options_json")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &options); err != nil {
			h.setFlash(w, r, "error", "Options must be valid JSON.")
			http.Redirect(w, r, "/ui/db-sets/"+setID.String(), http.StatusSeeOther)
			return
		}
	}

	target, err := store.CreateDBTarget(r.Context(), h.pool, h.secretKey, store.CreateTargetInput{
		DBSetID:  setID,
		Engine:   r.FormValue("engine"),
		Host:     r.FormValue("host"),
		Port:     port,
		DBName:   r.FormValue("dbname"),
		Username: r.FormValue("username"),
		Password: r.FormValue("password"),
		Options:  options,
	})
	if err != nil {
		h.setFlash(w, r, "error", err.Error())
		http.Redirect(w, r, "/ui/db-sets/"+setID.String(), http.StatusSeeOther)
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
	h.setFlash(w, r, "success", "DB target created.")
	http.Redirect(w, r, "/ui/db-sets/"+setID.String(), http.StatusSeeOther)
}

func (h *UIHandler) EditTarget(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	if user == nil {
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	targetID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.setFlash(w, r, "error", "Invalid target id.")
		http.Redirect(w, r, "/ui/db-sets", http.StatusSeeOther)
		return
	}
	target, _, err := store.GetDBTarget(r.Context(), h.pool, targetID)
	if err != nil {
		h.renderError(w, r, http.StatusNotFound, "Target not found.")
		return
	}
	set, err := store.GetDBSet(r.Context(), h.pool, target.DBSetID)
	if err != nil || set.ProjectID != *user.ProjectID {
		h.renderError(w, r, http.StatusNotFound, "Target not found.")
		return
	}

	var options map[string]any
	if raw := strings.TrimSpace(r.FormValue("options_json")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &options); err != nil {
			h.setFlash(w, r, "error", "Options must be valid JSON.")
			http.Redirect(w, r, "/ui/db-sets/"+target.DBSetID.String(), http.StatusSeeOther)
			return
		}
	}
	var portPtr *int
	if portStr := strings.TrimSpace(r.FormValue("port")); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			portPtr = &p
		}
	}
	host := strings.TrimSpace(r.FormValue("host"))
	dbname := strings.TrimSpace(r.FormValue("dbname"))
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	updated, err := store.UpdateDBTarget(r.Context(), h.pool, h.secretKey, targetID, store.UpdateTargetInput{
		Host:     strPtr(host),
		Port:     portPtr,
		DBName:   strPtr(dbname),
		Username: strPtr(username),
		Password: strPtr(password),
		Options:  options,
	})
	if err != nil {
		h.setFlash(w, r, "error", err.Error())
		http.Redirect(w, r, "/ui/db-sets/"+target.DBSetID.String(), http.StatusSeeOther)
		return
	}
	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "db_target_updated",
		EntityType: "db_target",
		EntityID:   &updated.ID,
		Payload: map[string]any{
			"db_set_id": updated.DBSetID,
			"engine":    updated.Engine,
			"host":      updated.Host,
			"port":      updated.Port,
		},
	})
	h.setFlash(w, r, "success", "DB target updated.")
	http.Redirect(w, r, "/ui/db-sets/"+updated.DBSetID.String(), http.StatusSeeOther)
}

func (h *UIHandler) DisableDBSet(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	if user == nil {
		return
	}
	if user.Role != rbac.RoleAdmin {
		h.renderError(w, r, http.StatusForbidden, "Admin role required.")
		return
	}
	setID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.setFlash(w, r, "error", "Invalid db set id.")
		http.Redirect(w, r, "/ui/db-sets", http.StatusSeeOther)
		return
	}
	set, err := store.GetDBSet(r.Context(), h.pool, setID)
	if err != nil {
		h.renderError(w, r, http.StatusNotFound, "DB set not found.")
		return
	}
	if err := store.DisableDBSet(r.Context(), h.pool, setID); err != nil {
		h.setFlash(w, r, "error", "Failed to disable db set.")
		http.Redirect(w, r, "/ui/db-sets/"+setID.String(), http.StatusSeeOther)
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
	h.setFlash(w, r, "success", "DB set disabled.")
	http.Redirect(w, r, "/ui/db-sets?env="+url.QueryEscape(set.Env), http.StatusSeeOther)
}

func (h *UIHandler) DisableTarget(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	if user == nil {
		return
	}
	if user.Role != rbac.RoleAdmin {
		h.renderError(w, r, http.StatusForbidden, "Admin role required.")
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	targetID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.setFlash(w, r, "error", "Invalid target id.")
		http.Redirect(w, r, "/ui/db-sets", http.StatusSeeOther)
		return
	}
	target, _, err := store.GetDBTarget(r.Context(), h.pool, targetID)
	if err != nil {
		h.renderError(w, r, http.StatusNotFound, "Target not found.")
		return
	}
	set, err := store.GetDBSet(r.Context(), h.pool, target.DBSetID)
	if err != nil || set.ProjectID != *user.ProjectID {
		h.renderError(w, r, http.StatusNotFound, "Target not found.")
		return
	}
	if err := store.DisableDBTarget(r.Context(), h.pool, targetID); err != nil {
		h.setFlash(w, r, "error", "Failed to disable target.")
		http.Redirect(w, r, "/ui/db-sets/"+target.DBSetID.String(), http.StatusSeeOther)
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
	h.setFlash(w, r, "success", "DB target disabled.")
	http.Redirect(w, r, "/ui/db-sets/"+target.DBSetID.String(), http.StatusSeeOther)
}

func (h *UIHandler) TestTarget(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	if user == nil {
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	targetID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.setFlash(w, r, "error", "Invalid target id.")
		http.Redirect(w, r, "/ui/db-sets", http.StatusSeeOther)
		return
	}
	target, _, err := store.GetDBTarget(r.Context(), h.pool, targetID)
	if err != nil {
		h.renderError(w, r, http.StatusNotFound, "Target not found.")
		return
	}
	set, err := store.GetDBSet(r.Context(), h.pool, target.DBSetID)
	if err != nil || set.ProjectID != *user.ProjectID {
		h.renderError(w, r, http.StatusNotFound, "Target not found.")
		return
	}
	err = store.TestTargetConnection(r.Context(), h.pool, h.secretKey, targetID)
	if err != nil {
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
		h.setFlash(w, r, "error", "Connection failed.")
		http.Redirect(w, r, "/ui/db-sets/"+target.DBSetID.String(), http.StatusSeeOther)
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
	h.setFlash(w, r, "success", "Connection successful.")
	http.Redirect(w, r, "/ui/db-sets/"+target.DBSetID.String(), http.StatusSeeOther)
}

func (h *UIHandler) MigrationsList(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	data, _ := h.baseData(w, r)
	if user == nil {
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	q := r.URL.Query().Get("q")
	envFilter := r.URL.Query().Get("env")
	statusFilter := r.URL.Query().Get("status")
	pendingOnly := r.URL.Query().Get("pending") == "1"

	migs, err := store.ListMigrations(r.Context(), h.pool, *user.ProjectID, q)
	if err != nil {
		h.renderError(w, r, http.StatusInternalServerError, "Failed to list migrations.")
		return
	}
	latest, _ := store.LatestRunsByMigrationEnv(r.Context(), h.pool, *user.ProjectID)

	rows := make([]migrationRow, 0, len(migs))
	for _, m := range migs {
		statuses := deriveEnvStatuses(m, latest[m.ID])
		row := migrationRow{
			Migration: m,
			Statuses:  statuses,
		}
		if pendingOnly && !row.HasPendingApproval() {
			continue
		}
		if envFilter != "" && statusFilter != "" {
			if s, ok := statuses[envFilter]; ok {
				if s.Label != statusFilter {
					continue
				}
			} else {
				continue
			}
		}
		rows = append(rows, row)
	}

	data.Page = migrationsPage{
		Migrations:   rows,
		Query:        q,
		EnvFilter:    envFilter,
		StatusFilter: statusFilter,
		PendingOnly:  pendingOnly,
	}
	h.renderer.Render(w, data)
}

func (h *UIHandler) MigrationNew(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	data, _ := h.baseData(w, r)
	if user == nil {
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	data.Page = migrationFormPage{}
	h.renderer.Render(w, data)
}

func (h *UIHandler) MigrationCreate(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	if user == nil {
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	sqlDown := strings.TrimSpace(r.FormValue("sql_down"))
	var sqlDownPtr *string
	if sqlDown != "" {
		sqlDownPtr = &sqlDown
	}
	mig, err := store.CreateMigration(r.Context(), h.pool, store.CreateMigrationInput{
		ProjectID:       *user.ProjectID,
		Key:             r.FormValue("key"),
		Name:            r.FormValue("name"),
		Jira:            r.FormValue("jira"),
		Description:     r.FormValue("description"),
		SQLUp:           r.FormValue("sql_up"),
		SQLDown:         sqlDownPtr,
		TransactionMode: r.FormValue("transaction_mode"),
		CreatedBy:       user.ID,
	})
	if err != nil {
		h.setFlash(w, r, "error", err.Error())
		http.Redirect(w, r, "/ui/migrations/new", http.StatusSeeOther)
		return
	}
	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "migration_created",
		EntityType: "migration",
		EntityID:   &mig.ID,
		Payload: map[string]any{
			"key":     mig.Key,
			"version": mig.Version,
		},
	})
	h.setFlash(w, r, "success", "Migration created.")
	http.Redirect(w, r, "/ui/migrations/"+mig.ID.String(), http.StatusSeeOther)
}

func (h *UIHandler) MigrationDetail(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	data, _ := h.baseData(w, r)
	if user == nil {
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid migration id.")
		return
	}
	mig, err := store.GetMigration(r.Context(), h.pool, *user.ProjectID, id)
	if err != nil {
		h.renderError(w, r, http.StatusNotFound, "Migration not found.")
		return
	}
	latest, _ := store.LatestRunsByMigrationEnv(r.Context(), h.pool, *user.ProjectID)
	statuses := deriveEnvStatuses(*mig, latest[mig.ID])

	dbSetsByEnv := map[string][]store.DBSet{}
	for _, env := range []string{"daily", "stg", "prd"} {
		sets, _ := store.ListDBSets(r.Context(), h.pool, *user.ProjectID, env)
		dbSetsByEnv[env] = sets
	}
	events, _ := store.ListTimelineEvents(r.Context(), h.pool, mig.ID)

	data.Page = migrationDetailPage{
		Migration: *mig,
		Statuses:  statuses,
		DBSets:    dbSetsByEnv,
		Events:    events,
	}
	h.renderer.Render(w, data)
}

func (h *UIHandler) MigrationUpdate(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	if user == nil {
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.setFlash(w, r, "error", "Invalid migration id.")
		http.Redirect(w, r, "/ui/migrations", http.StatusSeeOther)
		return
	}
	sqlDown := strings.TrimSpace(r.FormValue("sql_down"))
	sqlDownPtr := &sqlDown
	mig, sqlChanged, err := store.UpdateMigration(r.Context(), h.pool, *user.ProjectID, id, store.UpdateMigrationInput{
		Name:            stringPtr(r.FormValue("name")),
		Jira:            stringPtr(r.FormValue("jira")),
		Description:     stringPtr(r.FormValue("description")),
		SQLUp:           stringPtr(r.FormValue("sql_up")),
		SQLDown:         sqlDownPtr,
		TransactionMode: stringPtr(r.FormValue("transaction_mode")),
	})
	if err != nil {
		h.setFlash(w, r, "error", err.Error())
		http.Redirect(w, r, "/ui/migrations/"+id.String(), http.StatusSeeOther)
		return
	}
	if sqlChanged {
		_ = store.DeleteApprovalsForMigration(r.Context(), h.pool, mig.ID)
		_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
			ActorID:    &user.ID,
			Action:     "migration_approvals_invalidated",
			EntityType: "migration",
			EntityID:   &mig.ID,
			Payload: map[string]any{
				"version": mig.Version,
			},
		})
	}
	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     "migration_updated",
		EntityType: "migration",
		EntityID:   &mig.ID,
		Payload: map[string]any{
			"version":     mig.Version,
			"sql_changed": sqlChanged,
		},
	})
	h.setFlash(w, r, "success", "Migration updated.")
	http.Redirect(w, r, "/ui/migrations/"+id.String(), http.StatusSeeOther)
}

func (h *UIHandler) RequestApproval(w http.ResponseWriter, r *http.Request) {
	h.requestRun(w, r, "apply")
}

func (h *UIHandler) RequestRollback(w http.ResponseWriter, r *http.Request) {
	h.requestRun(w, r, "rollback")
}

func (h *UIHandler) Approvals(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	data, _ := h.baseData(w, r)
	if user == nil {
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	if user.Role != rbac.RoleManager && user.Role != rbac.RoleAdmin {
		h.renderError(w, r, http.StatusForbidden, "Manager or admin role required.")
		return
	}
	env := r.URL.Query().Get("env")
	runs, err := store.ListPendingApprovals(r.Context(), h.pool, *user.ProjectID, env)
	if err != nil {
		h.renderError(w, r, http.StatusInternalServerError, "Failed to list approvals.")
		return
	}
	data.Page = approvalsPage{Env: env, Runs: runs}
	h.renderer.Render(w, data)
}

func (h *UIHandler) Runs(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	data, _ := h.baseData(w, r)
	if user == nil {
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	filter := store.RunListFilter{
		Env:          r.URL.Query().Get("env"),
		Status:       r.URL.Query().Get("status"),
		MigrationKey: strings.ToLower(r.URL.Query().Get("migration_key")),
	}
	runs, err := store.ListRuns(r.Context(), h.pool, *user.ProjectID, filter, 100)
	if err != nil {
		h.renderError(w, r, http.StatusInternalServerError, "Failed to list runs.")
		return
	}
	data.Page = runsPage{
		Runs:   runs,
		Filter: filter,
	}
	h.renderer.Render(w, data)
}

func (h *UIHandler) RunDetail(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	data, _ := h.baseData(w, r)
	if user == nil {
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid run id.")
		return
	}
	run, err := store.GetRunWithItems(r.Context(), h.pool, *user.ProjectID, runID)
	if err != nil {
		h.renderError(w, r, http.StatusNotFound, "Run not found.")
		return
	}
	data.Page = runDetailPage{
		Run:              *run,
		IsManager:        user.Role == rbac.RoleManager || user.Role == rbac.RoleAdmin,
		RequestedByEmail: h.lookupEmail(r.Context(), run.RequestedBy),
		ApprovedByEmail:  h.lookupEmailPtr(r.Context(), run.ApprovedBy),
		ExecutedByEmail:  h.lookupEmailPtr(r.Context(), run.ExecutedBy),
	}
	h.renderer.Render(w, data)
}

func (h *UIHandler) RunItemLogs(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	data, _ := h.baseData(w, r)
	if user == nil {
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid run id.")
		return
	}
	itemID, err := uuid.Parse(chi.URLParam(r, "item_id"))
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid item id.")
		return
	}
	item, err := store.GetRunItemLog(r.Context(), h.pool, *user.ProjectID, runID, itemID)
	if err != nil {
		h.renderError(w, r, http.StatusNotFound, "Log not found.")
		return
	}
	data.Page = runLogsPage{
		RunID:   runID,
		Item:    *item,
		LogText: coalesceLog(item.Log),
	}
	h.renderer.Render(w, data)
}

func (h *UIHandler) ExecuteRun(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	if user == nil {
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.setFlash(w, r, "error", "Invalid run id.")
		http.Redirect(w, r, "/ui/runs", http.StatusSeeOther)
		return
	}
	run, err := h.callRunExecute(r, *user.ProjectID, runID, user.ID)
	if err != nil {
		_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
			ActorID:    &user.ID,
			Action:     "run_execute_failed",
			EntityType: "run",
			EntityID:   &runID,
			Payload: map[string]any{
				"error": err.Error(),
			},
		})
		h.setFlash(w, r, "error", err.Error())
		http.Redirect(w, r, "/ui/runs/"+runID.String(), http.StatusSeeOther)
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
	h.setFlash(w, r, "success", "Run execution started.")
	http.Redirect(w, r, "/ui/runs/"+runID.String(), http.StatusSeeOther)
}

func (h *UIHandler) ApproveRun(w http.ResponseWriter, r *http.Request) {
	h.runDecision(w, r, "approved")
}

func (h *UIHandler) DenyRun(w http.ResponseWriter, r *http.Request) {
	h.runDecision(w, r, "denied")
}

func (h *UIHandler) Logout(w http.ResponseWriter, r *http.Request) {
	user := mustUser(r)
	if user == nil {
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
		},
	})
	http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
}

// Helpers

func (h *UIHandler) baseData(w http.ResponseWriter, r *http.Request) (UIData, *auth.Session) {
	user := mustUser(r)
	if user == nil {
		return UIData{}, nil
	}
	session, err := h.sessions.GetSession(r)
	if err != nil {
		h.renderError(w, r, http.StatusUnauthorized, "Session invalid.")
		return UIData{}, nil
	}
	projects, _ := store.ListProjects(r.Context(), h.pool)
	var active *store.Project
	if user.ProjectID != nil {
		p, err := store.GetProject(r.Context(), h.pool, *user.ProjectID)
		if err == nil {
			active = p
		}
	}
	flash := session.Flash
	if flash != nil {
		session.Flash = nil
		_ = h.sessions.SetSession(w, *session)
	}
	return UIData{
		Title:         "migrate-hub",
		Template:      templateNameFromPath(r.URL.Path),
		User:          user,
		Projects:      projects,
		ActiveProject: active,
		CSRFToken:     user.CSRFToken,
		Flash:         flash,
		Path:          r.URL.Path,
	}, session
}

func (h *UIHandler) renderError(w http.ResponseWriter, r *http.Request, status int, message string) {
	w.WriteHeader(status)
	data := UIData{
		Title:    "Error",
		Template: "error",
		Path:     r.URL.Path,
		Page: errorPage{
			Status:  status,
			Message: message,
		},
	}
	h.renderer.Render(w, data)
}

func (h *UIHandler) setFlash(w http.ResponseWriter, r *http.Request, kind, message string) {
	session, err := h.sessions.GetSession(r)
	if err != nil {
		return
	}
	session.Flash = &auth.FlashMessage{Kind: kind, Message: message}
	_ = h.sessions.SetSession(w, *session)
}

func (h *UIHandler) requestRun(w http.ResponseWriter, r *http.Request, runType string) {
	user := mustUser(r)
	if user == nil {
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	migrationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.setFlash(w, r, "error", "Invalid migration id.")
		http.Redirect(w, r, "/ui/migrations", http.StatusSeeOther)
		return
	}
	env := r.FormValue("env")
	dbSetIDStr := r.FormValue("db_set_id")
	dbSetID, err := uuid.Parse(dbSetIDStr)
	if err != nil {
		h.setFlash(w, r, "error", "Invalid db set id.")
		http.Redirect(w, r, "/ui/migrations/"+migrationID.String(), http.StatusSeeOther)
		return
	}
	run, err := store.RequestRun(r.Context(), h.pool, store.RequestRunInput{
		ProjectID:   *user.ProjectID,
		MigrationID: migrationID,
		DBSetID:     dbSetID,
		Env:         env,
		RequestedBy: user.ID,
		RunType:     runType,
	})
	if err != nil {
		h.setFlash(w, r, "error", err.Error())
		http.Redirect(w, r, "/ui/migrations/"+migrationID.String(), http.StatusSeeOther)
		return
	}
	action := "run_requested"
	if runType == "rollback" {
		action = "rollback_requested"
	}
	_ = audit.LogEvent(r.Context(), h.pool, h.logger, audit.Event{
		ActorID:    &user.ID,
		Action:     action,
		EntityType: "run",
		EntityID:   &run.ID,
		Payload: map[string]any{
			"migration_id": run.MigrationID,
			"env":          run.Env,
			"db_set_id":    run.DBSetID,
		},
	})
	h.setFlash(w, r, "success", "Approval requested.")
	http.Redirect(w, r, "/ui/migrations/"+migrationID.String(), http.StatusSeeOther)
}

func (h *UIHandler) runDecision(w http.ResponseWriter, r *http.Request, decision string) {
	user := mustUser(r)
	if user == nil {
		return
	}
	if user.Role != rbac.RoleManager && user.Role != rbac.RoleAdmin {
		h.renderError(w, r, http.StatusForbidden, "Manager or admin role required.")
		return
	}
	if user.ProjectID == nil {
		h.setFlash(w, r, "error", "Select a project first.")
		http.Redirect(w, r, "/ui/projects", http.StatusSeeOther)
		return
	}
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.setFlash(w, r, "error", "Invalid run id.")
		http.Redirect(w, r, "/ui/approvals", http.StatusSeeOther)
		return
	}
	comment := r.FormValue("comment")
	input := store.ApprovalDecisionInput{
		RunID:     runID,
		ProjectID: *user.ProjectID,
		ActorID:   user.ID,
		Comment:   comment,
		Decision:  decision,
	}
	var run *store.Run
	if decision == "approved" {
		run, err = store.ApproveRun(r.Context(), h.pool, input)
	} else {
		run, err = store.DenyRun(r.Context(), h.pool, input)
	}
	if err != nil {
		h.setFlash(w, r, "error", err.Error())
		http.Redirect(w, r, "/ui/approvals", http.StatusSeeOther)
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
			"comment":      comment,
		},
	})
	h.setFlash(w, r, "success", "Run "+decision+".")
	http.Redirect(w, r, "/ui/approvals", http.StatusSeeOther)
}

func (h *UIHandler) callRunExecute(r *http.Request, projectID uuid.UUID, runID uuid.UUID, actorID uuid.UUID) (*store.RunWithItems, error) {
	run, err := store.GetRunWithItems(r.Context(), h.pool, projectID, runID)
	if err != nil {
		return nil, err
	}
	if run.Status != "approved" {
		return nil, errors.New("run is not approved")
	}
	return h.executor.ExecuteRun(r.Context(), projectID, runID, actorID)
}

func mustUser(r *http.Request) *auth.User {
	user, _ := auth.UserFromContext(r.Context())
	return user
}

func templateNameFromPath(path string) string {
	switch {
	case path == "/ui" || path == "/ui/":
		return "dashboard"
	case path == "/ui/login":
		return "login"
	case path == "/ui/projects":
		return "projects"
	case path == "/ui/targets":
		return "targets"
	case path == "/ui/users":
		return "users"
	case strings.HasPrefix(path, "/ui/db-sets/") && path != "/ui/db-sets":
		return "db_set_detail"
	case path == "/ui/db-sets":
		return "db_sets"
	case path == "/ui/migrations/new":
		return "migration_new"
	case strings.HasPrefix(path, "/ui/migrations/"):
		return "migration_detail"
	case path == "/ui/migrations":
		return "migrations"
	case path == "/ui/approvals":
		return "approvals"
	case strings.HasPrefix(path, "/ui/runs/") && strings.Contains(path, "/items/"):
		return "run_logs"
	case strings.HasPrefix(path, "/ui/runs/"):
		return "run_detail"
	case path == "/ui/runs":
		return "runs"
	default:
		return "dashboard"
	}
}

func deriveEnvStatuses(m store.Migration, runs map[string]store.Run) map[string]envStatus {
	out := make(map[string]envStatus)
	for _, env := range []string{"daily", "stg", "prd"} {
		run, ok := runs[env]
		if !ok {
			out[env] = envStatus{Label: "draft"}
			continue
		}
		status := envStatus{
			RunID:   &run.ID,
			RunType: run.RunType,
			Status:  run.Status,
		}
		if run.ChecksumUpAtRequest != m.ChecksumUp || !equalNullable(run.ChecksumDownAtRequest, m.ChecksumDown) {
			status.Label = "needs_reapproval"
			out[env] = status
			continue
		}
		label := run.Status
		switch run.Status {
		case "awaiting_approval":
			label = "awaiting_approve"
		case "executed":
			if run.RunType == "rollback" {
				label = "rollbacked"
			} else {
				label = "executed"
			}
		}
		status.Label = label
		out[env] = status
	}
	return out
}

func (r migrationRow) HasPendingApproval() bool {
	for _, status := range r.Statuses {
		if status.Label == "awaiting_approve" {
			return true
		}
	}
	return false
}

func stringPtr(s string) *string {
	return &s
}

func strPtr(s string) *string {
	val := strings.TrimSpace(s)
	if val == "" {
		return nil
	}
	return &val
}

func coalesceLog(log *string) string {
	if log == nil || strings.TrimSpace(*log) == "" {
		return "(no logs)"
	}
	return *log
}

func maskedDSN(target store.DBTarget) string {
	engine := strings.ToLower(target.Engine)
	host := target.Host
	port := target.Port
	db := target.DBName
	switch engine {
	case "mysql":
		return fmt.Sprintf("mysql://***:***@%s:%d/%s", host, port, db)
	default:
		return fmt.Sprintf("postgres://***:***@%s:%d/%s", host, port, db)
	}
}

func compareStatusFromLatest(item store.LatestRunItem) compareStatus {
	status := compareStatus{
		RunID:       &item.RunID,
		RunType:     item.RunType,
		RunStatus:   item.RunStatus,
		ItemStatus:  item.ItemStatus,
		RequestedAt: &item.RequestedAt,
	}
	switch item.RunStatus {
	case "executed":
		if item.RunType == "rollback" && item.ItemStatus == "executed" {
			status.Label = "rollbacked"
			return status
		}
		if item.ItemStatus == "executed" || item.ItemStatus == "skipped" {
			status.Label = "applied"
			return status
		}
		if item.ItemStatus == "failed" {
			status.Label = "failed"
			return status
		}
		status.Label = "executed"
	case "awaiting_approval":
		status.Label = "awaiting_approval"
	case "approved":
		status.Label = "approved"
	case "running":
		status.Label = "running"
	case "failed":
		status.Label = "failed"
	default:
		status.Label = item.RunStatus
	}
	return status
}

func (h *UIHandler) projectInventories(ctx context.Context, projects []store.Project) ([]projectInventory, error) {
	inventories := make([]projectInventory, 0, len(projects))
	for _, project := range projects {
		sets, err := store.ListDBSets(ctx, h.pool, project.ID, "")
		if err != nil {
			return nil, err
		}
		envSetCounts := map[string]int{}
		envTargetCounts := map[string]int{}
		var rows []projectTargetRow
		for _, set := range sets {
			envSetCounts[set.Env]++
			targets, err := store.ListDBTargetsBySet(ctx, h.pool, set.ID)
			if err != nil {
				return nil, err
			}
			for _, target := range targets {
				envTargetCounts[set.Env]++
				rows = append(rows, projectTargetRow{
					Env:       set.Env,
					DBSetName: set.Name,
					Engine:    target.Engine,
					Host:      target.Host,
					Port:      target.Port,
					DBName:    target.DBName,
					IsActive:  target.IsActive,
					MaskedDSN: maskedDSN(target),
				})
			}
		}
		summaries := make([]envSummary, 0, 3)
		for _, env := range []string{"daily", "stg", "prd"} {
			summaries = append(summaries, envSummary{
				Env:         env,
				SetCount:    envSetCounts[env],
				TargetCount: envTargetCounts[env],
			})
		}
		inventories = append(inventories, projectInventory{
			Project:      project,
			EnvSummaries: summaries,
			Targets:      rows,
		})
	}
	return inventories, nil
}

type dashboardPage struct {
	PendingCount int
	RunningCount int
	FailedCount  int
	RecentRuns   []store.RunSummary
	NeedsProject bool
}

type projectsPage struct {
	IsAdmin     bool
	Inventories []projectInventory
}

type usersPage struct {
	Users []store.UserRecord
}

type projectInventory struct {
	Project      store.Project
	EnvSummaries []envSummary
	Targets      []projectTargetRow
}

type envSummary struct {
	Env         string
	SetCount    int
	TargetCount int
}

type projectTargetRow struct {
	Env       string
	DBSetName string
	Engine    string
	Host      string
	Port      int
	DBName    string
	IsActive  bool
	MaskedDSN string
}

type dbSetsPage struct {
	Env     string
	DBSets  []store.DBSet
	IsAdmin bool
}

type dbSetDetailPage struct {
	DBSet   store.DBSet
	Targets []store.DBTarget
	IsAdmin bool
}

type targetsPage struct {
	Env         string
	OnlyMissing bool
	Targets     []targetMigrationView
}

type envStatus struct {
	Label   string
	RunID   *uuid.UUID
	RunType string
	Status  string
}

type migrationRow struct {
	Migration store.Migration
	Statuses  map[string]envStatus
}

type migrationsPage struct {
	Migrations   []migrationRow
	Query        string
	EnvFilter    string
	StatusFilter string
	PendingOnly  bool
}

type migrationFormPage struct{}

type migrationDetailPage struct {
	Migration store.Migration
	Statuses  map[string]envStatus
	DBSets    map[string][]store.DBSet
	Events    []store.TimelineEvent
}

type approvalsPage struct {
	Env  string
	Runs []store.RunSummary
}

type runsPage struct {
	Runs   []store.RunSummary
	Filter store.RunListFilter
}

type runDetailPage struct {
	Run              store.RunWithItems
	IsManager        bool
	RequestedByEmail string
	ApprovedByEmail  string
	ExecutedByEmail  string
}

type runLogsPage struct {
	RunID   uuid.UUID
	Item    store.RunItem
	LogText string
}

type targetMigrationView struct {
	DBSet           store.DBSet
	Target          store.DBTarget
	MaskedDSN       string
	AppliedCount    int
	MissingCount    int
	RollbackedCount int
	ApprovedCount   int
	AwaitingCount   int
	RunningCount    int
	FailedCount     int
	Rows            []targetMigrationRow
}

type targetMigrationRow struct {
	Migration store.Migration
	Status    compareStatus
}

type compareStatus struct {
	Label       string
	RunID       *uuid.UUID
	RunType     string
	RunStatus   string
	ItemStatus  string
	RequestedAt *time.Time
}

type errorPage struct {
	Status  int
	Message string
}

func (h *UIHandler) lookupEmail(ctx context.Context, id uuid.UUID) string {
	user, err := store.GetUserByID(ctx, h.pool, id)
	if err != nil {
		return "-"
	}
	return user.Email
}

func (h *UIHandler) lookupEmailPtr(ctx context.Context, id *uuid.UUID) string {
	if id == nil {
		return "-"
	}
	return h.lookupEmail(ctx, *id)
}

func equalNullable(a *string, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
