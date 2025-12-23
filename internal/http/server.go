package httpserver

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"db_inner_migrator_syncer/internal/auth"
	"db_inner_migrator_syncer/internal/config"
	"db_inner_migrator_syncer/internal/rbac"
	"db_inner_migrator_syncer/web"
)

type Server struct {
	cfg              config.Config
	logger           requestLogger
	db               *pgxpool.Pool
	authn            auth.Authenticator
	authHandler      *AuthHandler
	projectHandler   *ProjectHandler
	dbHandler        *DBInventoryHandler
	migrationHandler *MigrationHandler
	runHandler       *RunHandler
	uiHandler        *UIHandler
}

type requestLogger interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
}

func New(cfg config.Config, logger requestLogger, db *pgxpool.Pool, authn auth.Authenticator, authHandler *AuthHandler, projectHandler *ProjectHandler, dbHandler *DBInventoryHandler, migrationHandler *MigrationHandler, runHandler *RunHandler, uiHandler *UIHandler) *Server {
	return &Server{
		cfg:              cfg,
		logger:           logger,
		db:               db,
		authn:            authn,
		authHandler:      authHandler,
		projectHandler:   projectHandler,
		dbHandler:        dbHandler,
		migrationHandler: migrationHandler,
		runHandler:       runHandler,
		uiHandler:        uiHandler,
	}
}

func (s *Server) Start(ctx context.Context) error {
	r := s.routes()
	httpServer := &http.Server{
		Addr:              s.cfg.HTTPAddress,
		Handler:           r,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		s.logger.Info("http server starting", "addr", s.cfg.HTTPAddress)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.logger.Info("http server shutting down")
		return httpServer.Shutdown(shutdownCtx)
	case err := <-serverErr:
		return err
	}
}

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(RequestLogger(s.logger))

	authMiddleware := NewAuthMiddleware(s.authn, s.db, s.logger)

	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(web.StaticFS()))))

	r.Route("/api/v1", func(api chi.Router) {
		api.Method(http.MethodGet, "/health", HealthHandler{DB: s.db})

		api.Get("/auth/google/start", s.authHandler.GoogleStart)
		api.Get("/auth/google/callback", s.authHandler.GoogleCallback)

		// Authenticated read-only routes
		api.Group(func(authenticated chi.Router) {
			authenticated.Use(authMiddleware.RequireAuth)
			authenticated.Use(authMiddleware.RequireRoles(rbac.RoleUser, rbac.RoleManager, rbac.RoleAdmin))
			authenticated.Get("/me", func(w http.ResponseWriter, r *http.Request) {
				user, _ := auth.UserFromContext(r.Context())
				writeJSON(w, http.StatusOK, map[string]any{
					"id":         user.ID,
					"email":      user.Email,
					"name":       user.Name,
					"role":       user.Role,
					"project_id": user.ProjectID,
				})
			})
			authenticated.Get("/projects", s.projectHandler.List)
			authenticated.Get("/db-sets", s.dbHandler.ListDBSets)
			authenticated.Get("/db-sets/{id}/targets", s.dbHandler.ListTargets)
			authenticated.Get("/targets/{id}", s.dbHandler.GetTarget)
			authenticated.Get("/migrations", s.migrationHandler.List)
			authenticated.Get("/migrations/{id}", s.migrationHandler.Get)
			authenticated.Get("/runs/{id}", s.runHandler.Get)
			authenticated.Get("/migrations/{id}/runs", s.runHandler.ListForMigration)
		})

		// Authenticated state-changing routes (CSRF protected)
		api.Group(func(authenticated chi.Router) {
			authenticated.Use(authMiddleware.RequireAuth)
			authenticated.Use(CSRFMiddleware)

			authenticated.Post("/auth/logout", s.authHandler.Logout)

			authenticated.Route("/projects", func(pr chi.Router) {
				pr.With(authMiddleware.RequireRoles(rbac.RoleAdmin)).Post("/", s.projectHandler.Create)
				pr.Post("/{id}/select", s.projectHandler.Select)
			})

			authenticated.Route("/db-sets", func(ds chi.Router) {
				ds.With(authMiddleware.RequireRoles(rbac.RoleUser, rbac.RoleManager, rbac.RoleAdmin)).Post("/", s.dbHandler.CreateDBSet)
				ds.With(authMiddleware.RequireRoles(rbac.RoleAdmin)).Post("/{id}/disable", s.dbHandler.DisableDBSet)
				ds.Route("/{id}/targets", func(tr chi.Router) {
					tr.With(authMiddleware.RequireRoles(rbac.RoleUser, rbac.RoleManager, rbac.RoleAdmin)).Post("/", s.dbHandler.CreateTarget)
				})
			})

			authenticated.Route("/targets", func(tr chi.Router) {
				tr.With(authMiddleware.RequireRoles(rbac.RoleUser, rbac.RoleManager, rbac.RoleAdmin)).Post("/{id}/test-connection", s.dbHandler.TestConnection)
				tr.With(authMiddleware.RequireRoles(rbac.RoleAdmin)).Post("/{id}/disable", s.dbHandler.DisableTarget)
			})

			authenticated.Route("/migrations", func(mg chi.Router) {
				mg.With(authMiddleware.RequireRoles(rbac.RoleUser, rbac.RoleManager, rbac.RoleAdmin)).Post("/", s.migrationHandler.Create)
				mg.With(authMiddleware.RequireRoles(rbac.RoleUser, rbac.RoleManager, rbac.RoleAdmin)).Patch("/{id}", s.migrationHandler.Update)
				mg.With(authMiddleware.RequireRoles(rbac.RoleUser, rbac.RoleManager, rbac.RoleAdmin)).Post("/{id}/request-approval", s.runHandler.RequestApproval)
				mg.With(authMiddleware.RequireRoles(rbac.RoleUser, rbac.RoleManager, rbac.RoleAdmin)).Post("/{id}/request-rollback", s.runHandler.RequestRollback)
			})

			authenticated.Route("/runs", func(rn chi.Router) {
				rn.With(authMiddleware.RequireRoles(rbac.RoleManager, rbac.RoleAdmin)).Post("/{id}/approve", s.runHandler.Approve)
				rn.With(authMiddleware.RequireRoles(rbac.RoleManager, rbac.RoleAdmin)).Post("/{id}/deny", s.runHandler.Deny)
				rn.With(authMiddleware.RequireRoles(rbac.RoleUser, rbac.RoleManager, rbac.RoleAdmin)).Post("/{id}/execute", s.runHandler.Execute)
			})
		})
	})

	r.Route("/ui", func(ui chi.Router) {
		ui.Get("/login", s.uiHandler.Login)

		ui.Group(func(authed chi.Router) {
			authed.Use(s.uiHandler.RequireAuth)
			authed.Use(CSRFMiddleware)

			authed.Get("/", s.uiHandler.Dashboard)
			authed.Get("/projects", s.uiHandler.Projects)
			authed.Post("/projects", s.uiHandler.CreateProject)
			authed.Post("/projects/select", s.uiHandler.SelectProject)

			authed.Get("/targets", s.uiHandler.TargetMigrations)

			authed.Get("/users", s.uiHandler.Users)
			authed.Post("/users", s.uiHandler.CreateUser)
			authed.Post("/users/{id}/update", s.uiHandler.UpdateUser)
			authed.Post("/users/{id}/disable", s.uiHandler.DisableUser)

			authed.Get("/db-sets", s.uiHandler.DBSetList)
			authed.Post("/db-sets", s.uiHandler.CreateDBSet)
			authed.Get("/db-sets/{id}", s.uiHandler.DBSetDetail)
			authed.Post("/db-sets/{id}/disable", s.uiHandler.DisableDBSet)
			authed.Post("/db-sets/{id}/targets", s.uiHandler.AddTarget)

			authed.Post("/targets/{id}/edit", s.uiHandler.EditTarget)
			authed.Post("/targets/{id}/disable", s.uiHandler.DisableTarget)
			authed.Post("/targets/{id}/test-connection", s.uiHandler.TestTarget)

			authed.Get("/migrations", s.uiHandler.MigrationsList)
			authed.Get("/migrations/new", s.uiHandler.MigrationNew)
			authed.Post("/migrations/new", s.uiHandler.MigrationCreate)
			authed.Get("/migrations/{id}", s.uiHandler.MigrationDetail)
			authed.Post("/migrations/{id}/edit", s.uiHandler.MigrationUpdate)
			authed.Post("/migrations/{id}/request-approval", s.uiHandler.RequestApproval)
			authed.Post("/migrations/{id}/request-rollback", s.uiHandler.RequestRollback)

			authed.Get("/approvals", s.uiHandler.Approvals)
			authed.Post("/runs/{id}/approve", s.uiHandler.ApproveRun)
			authed.Post("/runs/{id}/deny", s.uiHandler.DenyRun)

			authed.Get("/runs", s.uiHandler.Runs)
			authed.Get("/runs/{id}", s.uiHandler.RunDetail)
			authed.Post("/runs/{id}/execute", s.uiHandler.ExecuteRun)
			authed.Get("/runs/{id}/items/{item_id}/logs", s.uiHandler.RunItemLogs)

			authed.Post("/logout", s.uiHandler.Logout)
		})
	})

	return r
}
