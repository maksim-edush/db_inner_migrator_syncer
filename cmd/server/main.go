package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"db_inner_migrator_syncer/internal/audit"
	"db_inner_migrator_syncer/internal/auth"
	"db_inner_migrator_syncer/internal/config"
	"db_inner_migrator_syncer/internal/executor"
	httpserver "db_inner_migrator_syncer/internal/http"
	"db_inner_migrator_syncer/internal/logging"
	"db_inner_migrator_syncer/internal/migrate"
	"db_inner_migrator_syncer/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	logger := logging.NewLogger(cfg.LogLevel)

	dbPool, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("db connection failed", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	migrator := migrate.New(dbPool, logger)
	if err := migrator.Up(ctx); err != nil {
		logger.Error("migrations failed", "error", err)
		os.Exit(1)
	}

	_ = logStartupEvent(ctx, dbPool, logger, cfg)

	sessions := auth.NewSessionManager(cfg.SecretKeyBytes)

	oidcProvider, err := auth.NewOIDCProvider(ctx, cfg)
	if err != nil {
		logger.Error("oidc provider init failed", "error", err)
		os.Exit(1)
	}

	sessionAuth := auth.NewSessionAuthenticator(sessions, dbPool)
	authenticators := []auth.Authenticator{sessionAuth}
	if os.Getenv("MIGRATEHUB_DEV_AUTH") == "true" {
		authenticators = append(authenticators, auth.NewDevHeaderAuthenticator(true))
	}
	authenticator := auth.NewMultiAuthenticator(authenticators...)

	authHandler := httpserver.NewAuthHandler(cfg, logger, oidcProvider, sessions, dbPool)
	projectHandler := httpserver.NewProjectHandler(dbPool, logger, sessions)
	dbHandler := httpserver.NewDBInventoryHandler(dbPool, logger, sessions, cfg.SecretKeyBytes)
	migrationHandler := httpserver.NewMigrationHandler(dbPool, logger)
	exec := executor.New(dbPool, cfg.SecretKeyBytes, logger)
	runHandler := httpserver.NewRunHandler(dbPool, logger, exec)
	renderer := httpserver.NewTemplateRenderer()
	uiHandler := httpserver.NewUIHandler(dbPool, logger, sessions, authenticator, renderer, cfg.SecretKeyBytes, exec)
	server := httpserver.New(cfg, logger, dbPool, authenticator, authHandler, projectHandler, dbHandler, migrationHandler, runHandler, uiHandler)

	if err := server.Start(ctx); err != nil {
		logger.Error("server stopped with error", "error", err)
		os.Exit(1)
	}
}

func logStartupEvent(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, cfg config.Config) error {
	return audit.LogEvent(ctx, pool, logger, audit.Event{
		Action:     "server_started",
		EntityType: "system",
		Payload: map[string]any{
			"http_addr": cfg.HTTPAddress,
			"ts":        time.Now().UTC(),
		},
	})
}
