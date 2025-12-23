package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"db_inner_migrator_syncer/migrations"
)

type Runner struct {
	pool   *pgxpool.Pool
	logger Logger
	fs     fs.FS
}

type Logger interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
}

func New(pool *pgxpool.Pool, logger Logger) *Runner {
	return &Runner{
		pool:   pool,
		logger: logger,
		fs:     migrations.FS(),
	}
}

func (r *Runner) Up(ctx context.Context) error {
	if err := r.ensureTable(ctx); err != nil {
		return err
	}

	applied, err := r.appliedVersions(ctx)
	if err != nil {
		return err
	}

	files, err := fs.Glob(r.fs, "*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(files)

	for _, file := range files {
		version, name, err := parseVersion(file)
		if err != nil {
			return err
		}
		if applied[version] {
			continue
		}
		body, err := fs.ReadFile(r.fs, file)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", file, err)
		}
		if err := r.apply(ctx, version, name, string(body)); err != nil {
			return fmt.Errorf("apply migration %s: %w", file, err)
		}
		r.logger.Info("migration applied", "version", version, "name", name)
	}
	return nil
}

func (r *Runner) apply(ctx context.Context, version int64, name string, body string) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	if _, err := tx.Exec(ctx, body); err != nil {
		return fmt.Errorf("execute migration: %w", err)
	}

	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version, name) VALUES ($1, $2)`, version, name); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}

	return tx.Commit(ctx)
}

func (r *Runner) ensureTable(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version BIGINT PRIMARY KEY,
  name    TEXT NOT NULL,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`)
	return err
}

func (r *Runner) appliedVersions(ctx context.Context) (map[int64]bool, error) {
	rows, err := r.pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("query schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int64]bool)
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan applied version: %w", err)
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func parseVersion(path string) (int64, string, error) {
	base := filepath.Base(path)
	parts := strings.SplitN(base, "_", 2)
	if len(parts) < 2 {
		return 0, "", fmt.Errorf("invalid migration filename: %s", base)
	}
	version, err := strconv.ParseInt(strings.TrimSuffix(parts[0], ".sql"), 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("invalid migration version in %s: %w", base, err)
	}
	name := strings.TrimSuffix(parts[1], ".sql")
	return version, name, nil
}
