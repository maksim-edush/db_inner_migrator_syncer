package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"db_inner_migrator_syncer/internal/secret"
	"db_inner_migrator_syncer/internal/store"
)

type Logger interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
}

type Executor struct {
	pool      *pgxpool.Pool
	secretKey []byte
	logger    Logger
}

func New(pool *pgxpool.Pool, secretKey []byte, logger Logger) *Executor {
	return &Executor{pool: pool, secretKey: secretKey, logger: logger}
}

func (e *Executor) ExecuteRun(ctx context.Context, projectID uuid.UUID, runID uuid.UUID, actorID uuid.UUID) (*store.RunWithItems, error) {
	run, err := store.GetRunWithItems(ctx, e.pool, projectID, runID)
	if err != nil {
		return nil, err
	}
	if run.Status != "approved" {
		return nil, errors.New("run must be approved before execution")
	}

	mig, err := store.GetMigration(ctx, e.pool, projectID, run.MigrationID)
	if err != nil {
		return nil, err
	}
	if mig.ChecksumUp != run.ChecksumUpAtRequest || !equalNullable(mig.ChecksumDown, run.ChecksumDownAtRequest) {
		return nil, store.ErrChecksumMismatch
	}

	now := time.Now().UTC()
	if _, err := e.pool.Exec(ctx, `
UPDATE runs
SET status = 'running', executed_by = $1, started_at = $2
WHERE id = $3
`, actorID, now, run.ID); err != nil {
		return nil, err
	}

	run.Status = "running"
	run.StartedAt = &now
	run.ExecutedBy = &actorID

	var firstErr error
	for i := range run.Items {
		item := &run.Items[i]
		if item.Status != "queued" {
			continue
		}
		if err := e.updateRunItemStatus(ctx, item.ID, "running", nil, nil); err != nil {
			firstErr = err
			break
		}
		item.Status = "running"
		start := time.Now().UTC()
		item.StartedAt = &start

		err := e.executeItem(ctx, run.Run, *item, *mig)
		if errors.Is(err, store.ErrAlreadyApplied) {
			end := time.Now().UTC()
			msg := "already applied, skipped"
			_ = e.updateRunItemStatus(ctx, item.ID, "skipped", &msg, &end)
			item.Status = "skipped"
			item.FinishedAt = &end
			item.Error = nil
			continue
		}
		if err != nil {
			firstErr = err
			end := time.Now().UTC()
			msg := err.Error()
			_ = e.updateRunItemStatus(ctx, item.ID, "failed", &msg, &end)
			item.Status = "failed"
			item.Error = &msg
			item.FinishedAt = &end
			break
		}
		end := time.Now().UTC()
		_ = e.updateRunItemStatus(ctx, item.ID, "executed", nil, &end)
		item.Status = "executed"
		item.FinishedAt = &end
	}

	finish := time.Now().UTC()
	if firstErr != nil {
		_, _ = e.pool.Exec(ctx, `
UPDATE runs SET status = 'failed', finished_at = $1 WHERE id = $2
`, finish, run.ID)
		run.Status = "failed"
		run.FinishedAt = &finish
		return run, firstErr
	}

	_, _ = e.pool.Exec(ctx, `
UPDATE runs SET status = 'executed', finished_at = $1 WHERE id = $2
`, finish, run.ID)
	run.Status = "executed"
	run.FinishedAt = &finish
	return run, nil
}

func (e *Executor) executeItem(ctx context.Context, run store.Run, item store.RunItem, mig store.Migration) error {
	target, encPwd, err := store.GetDBTarget(ctx, e.pool, item.DBTargetID)
	if err != nil {
		return err
	}
	if !target.IsActive {
		return errors.New("target disabled")
	}
	password, err := secret.Decrypt(e.secretKey, encPwd)
	if err != nil {
		return fmt.Errorf("decrypt password: %w", err)
	}

	switch strings.ToLower(target.Engine) {
	case "postgres":
		return e.execPostgres(ctx, run, item, mig, target, string(password))
	case "mysql":
		return e.execMySQL(ctx, run, item, mig, target, string(password))
	default:
		return store.ErrDBTargetBadEngine
	}
}

func (e *Executor) execPostgres(ctx context.Context, run store.Run, item store.RunItem, mig store.Migration, target *store.DBTarget, password string) error {
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", url.QueryEscape(target.Username), url.QueryEscape(password), target.Host, target.Port, url.PathEscape(target.DBName))
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	lockID := advisoryKey(target.ID)
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, lockID) // nolint:errcheck

	if err := ensureTargetMigrationsTablePg(ctx, conn); err != nil {
		return err
	}

	var existingChecksum string
	err = conn.QueryRow(ctx, `SELECT checksum_up FROM migrate_hub_migrations WHERE migration_key = $1`, mig.Key).Scan(&existingChecksum)
	if err == nil {
		if existingChecksum == mig.ChecksumUp {
			return store.ErrAlreadyApplied
		}
		return errors.New("migration already applied with different checksum")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	appliedBy := ""
	if run.ExecutedBy != nil {
		appliedBy = run.ExecutedBy.String()
	}

	applyFn := func(exec interface {
		Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	}) error {
		if _, err := exec.Exec(ctx, mig.SQLUp); err != nil {
			return err
		}
		_, err := exec.Exec(ctx, `
INSERT INTO migrate_hub_migrations (migration_key, checksum_up, checksum_down, applied_at, applied_by, tool_run_id)
VALUES ($1, $2, $3, now(), $4, $5)
`, mig.Key, mig.ChecksumUp, mig.ChecksumDown, appliedBy, run.ID.String())
		return err
	}

	switch mig.TransactionMode {
	case "no_transaction":
		return applyFn(conn)
	case "single_transaction", "auto":
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if err := applyFn(tx); err != nil {
			tx.Rollback(ctx) // nolint:errcheck
			return err
		}
		return tx.Commit(ctx)
	default:
		return store.ErrTxModeInvalid
	}
}

func (e *Executor) execMySQL(ctx context.Context, run store.Run, item store.RunItem, mig store.Migration, target *store.DBTarget, password string) error {
	cfg := mysql.Config{
		User:                 target.Username,
		Passwd:               password,
		Net:                  "tcp",
		Addr:                 net.JoinHostPort(target.Host, fmt.Sprintf("%d", target.Port)),
		DBName:               target.DBName,
		AllowNativePasswords: true,
		Params:               map[string]string{},
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetConnMaxLifetime(time.Minute)

	lockName := "migrate-hub:" + target.ID.String()
	var got int
	if err := db.QueryRowContext(ctx, `SELECT GET_LOCK(?, 10)`, lockName).Scan(&got); err != nil {
		return fmt.Errorf("get lock: %w", err)
	}
	if got != 1 {
		return errors.New("could not acquire lock")
	}
	defer db.ExecContext(ctx, `SELECT RELEASE_LOCK(?)`, lockName) // nolint:errcheck

	if err := ensureTargetMigrationsTableMySQL(ctx, db); err != nil {
		return err
	}

	var existingChecksum string
	err = db.QueryRowContext(ctx, `SELECT checksum_up FROM migrate_hub_migrations WHERE migration_key = ?`, mig.Key).Scan(&existingChecksum)
	if err == nil {
		if existingChecksum == mig.ChecksumUp {
			return store.ErrAlreadyApplied
		}
		return errors.New("migration already applied with different checksum")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	appliedBy := ""
	if run.ExecutedBy != nil {
		appliedBy = run.ExecutedBy.String()
	}

	applyFn := func(exec interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	}) error {
		if _, err := exec.ExecContext(ctx, mig.SQLUp); err != nil {
			return err
		}
		_, err := exec.ExecContext(ctx, `
INSERT INTO migrate_hub_migrations (migration_key, checksum_up, checksum_down, applied_at, applied_by, tool_run_id)
VALUES (?, ?, ?, NOW(), ?, ?)
`, mig.Key, mig.ChecksumUp, mig.ChecksumDown, appliedBy, run.ID.String())
		return err
	}

	switch mig.TransactionMode {
	case "no_transaction":
		return applyFn(db)
	case "single_transaction", "auto":
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := applyFn(tx); err != nil {
			tx.Rollback() // nolint:errcheck
			return err
		}
		return tx.Commit()
	default:
		return store.ErrTxModeInvalid
	}
}

func ensureTargetMigrationsTablePg(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, `
CREATE TABLE IF NOT EXISTS migrate_hub_migrations (
  migration_key TEXT PRIMARY KEY,
  checksum_up TEXT NOT NULL,
  checksum_down TEXT,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  applied_by TEXT,
  tool_run_id TEXT
)
`)
	return err
}

func ensureTargetMigrationsTableMySQL(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS migrate_hub_migrations (
  migration_key VARCHAR(255) PRIMARY KEY,
  checksum_up TEXT NOT NULL,
  checksum_down TEXT,
  applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  applied_by VARCHAR(255),
  tool_run_id VARCHAR(255)
)`)
	return err
}

func (e *Executor) updateRunItemStatus(ctx context.Context, itemID uuid.UUID, status string, errMsg *string, finishedAt *time.Time) error {
	_, err := e.pool.Exec(ctx, `
UPDATE run_items SET status = COALESCE($2, status), error = COALESCE($3, error), finished_at = COALESCE($4, finished_at), started_at = COALESCE(started_at, now())
WHERE id = $1
`, itemID, status, errMsg, finishedAt)
	return err
}

func advisoryKey(id uuid.UUID) int64 {
	var out int64
	bytes := id[:]
	for i := 0; i < 8; i++ {
		out = (out << 8) | int64(bytes[i])
	}
	return out
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
