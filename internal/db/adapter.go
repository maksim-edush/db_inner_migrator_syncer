package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"db_inner_migrator_syncer/internal/config"
)

// Adapter abstracts provider-specific behavior.
type Adapter interface {
	Provider() string
	Close() error
	EnsureMigrationTable(ctx context.Context, table string) error
	InsertStatus(ctx context.Context, table string, entry MigrationEntry) error
	UpdateStatus(ctx context.Context, table string, entry MigrationEntry) error
	FetchStatuses(ctx context.Context, table string, limit int) ([]MigrationEntry, error)
	ExecScript(ctx context.Context, script string) error
	FetchSchema(ctx context.Context, schema string) (Schema, error)
}

// Open builds an adapter for the given configuration.
func Open(cfg config.DBConfig) (Adapter, error) {
	provider := strings.ToLower(cfg.Provider)
	switch provider {
	case "postgres":
		db, err := sql.Open("pgx", cfg.DSN)
		if err != nil {
			return nil, err
		}
		db.SetConnMaxIdleTime(5 * time.Minute)
		db.SetMaxOpenConns(5)
		return &PostgresAdapter{db: db}, nil
	case "mysql":
		// Validate DSN early to provide actionable errors.
		if _, err := mysql.ParseDSN(cfg.DSN); err != nil {
			return nil, fmt.Errorf("invalid mysql dsn: %w", err)
		}
		db, err := sql.Open("mysql", cfg.DSN)
		if err != nil {
			return nil, err
		}
		db.SetConnMaxIdleTime(5 * time.Minute)
		db.SetMaxOpenConns(5)
		return &MySQLAdapter{db: db}, nil
	default:
		return nil, fmt.Errorf("unsupported provider %s", cfg.Provider)
	}
}
