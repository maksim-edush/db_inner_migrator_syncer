package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"db_inner_migrator_syncer/internal/config"
	"db_inner_migrator_syncer/internal/db"
)

// Runner executes migrations against a staging/production pair.
type Runner struct {
	Pair config.PairConfig
}

// Apply runs the forward migration against staging and then production.
func (r Runner) Apply(ctx context.Context, migrationName, forwardSQL, rollbackSQL, scriptPath, rollbackPath string, autoRollback bool) error {
	staging, err := db.Open(r.Pair.Staging)
	if err != nil {
		return fmt.Errorf("staging connection: %w", err)
	}
	defer staging.Close()
	prod, err := db.Open(r.Pair.Production)
	if err != nil {
		return fmt.Errorf("production connection: %w", err)
	}
	defer prod.Close()

	if err := staging.EnsureMigrationTable(ctx, r.Pair.MigrationTable); err != nil {
		return fmt.Errorf("ensure staging status table: %w", err)
	}
	if err := prod.EnsureMigrationTable(ctx, r.Pair.MigrationTable); err != nil {
		return fmt.Errorf("ensure production status table: %w", err)
	}

	checksum := dbChecksum(forwardSQL, rollbackSQL)
	if err := applySingle(ctx, staging, r.Pair.MigrationTable, migrationName, "staging", forwardSQL, rollbackSQL, scriptPath, rollbackPath, checksum, autoRollback); err != nil {
		return fmt.Errorf("staging apply: %w", err)
	}
	if err := applySingle(ctx, prod, r.Pair.MigrationTable, migrationName, "production", forwardSQL, rollbackSQL, scriptPath, rollbackPath, checksum, autoRollback); err != nil {
		return fmt.Errorf("production apply: %w", err)
	}
	return nil
}

// Rollback executes only the rollback script against a single environment.
func (r Runner) Rollback(ctx context.Context, env, name, rollbackSQL, rollbackPath string) error {
	adapterCfg, err := r.pickEnv(env)
	if err != nil {
		return err
	}
	adapter, err := db.Open(adapterCfg)
	if err != nil {
		return err
	}
	defer adapter.Close()
	if err := adapter.EnsureMigrationTable(ctx, r.Pair.MigrationTable); err != nil {
		return err
	}
	entry := db.MigrationEntry{
		MigrationName: name,
		ScriptFile:    rollbackPath,
		RollbackFile:  rollbackPath,
		Status:        "rolling_back",
		AppliedEnv:    env,
		AppliedAt:     time.Now().UTC(),
		Checksum:      dbChecksum("", rollbackSQL),
	}
	if err := adapter.InsertStatus(ctx, r.Pair.MigrationTable, entry); err != nil {
		return err
	}
	if err := adapter.ExecScript(ctx, rollbackSQL); err != nil {
		entry.Status = "rollback_failed"
		entry.Error = sql.NullString{Valid: true, String: err.Error()}
		_ = adapter.UpdateStatus(ctx, r.Pair.MigrationTable, entry)
		return err
	}
	entry.Status = "rolled_back"
	entry.AppliedAt = time.Now().UTC()
	return adapter.UpdateStatus(ctx, r.Pair.MigrationTable, entry)
}

// ApplyEnv runs the forward migration against a single environment (staging or production).
func (r Runner) ApplyEnv(ctx context.Context, env, migrationName, forwardSQL, rollbackSQL, scriptPath, rollbackPath string, autoRollback bool) error {
	targetCfg, err := r.pickEnv(env)
	if err != nil {
		return err
	}
	adapter, err := db.Open(targetCfg)
	if err != nil {
		return fmt.Errorf("%s connection: %w", env, err)
	}
	defer adapter.Close()

	if err := adapter.EnsureMigrationTable(ctx, r.Pair.MigrationTable); err != nil {
		return fmt.Errorf("ensure %s status table: %w", env, err)
	}

	checksum := dbChecksum(forwardSQL, rollbackSQL)
	if err := applySingle(ctx, adapter, r.Pair.MigrationTable, migrationName, env, forwardSQL, rollbackSQL, scriptPath, rollbackPath, checksum, autoRollback); err != nil {
		return fmt.Errorf("%s apply: %w", env, err)
	}
	return nil
}

func applySingle(ctx context.Context, adapter db.Adapter, table, migrationName, env, forwardSQL, rollbackSQL, scriptPath, rollbackPath, checksum string, autoRollback bool) error {
	entry := db.MigrationEntry{
		MigrationName: migrationName,
		ScriptFile:    scriptPath,
		RollbackFile:  rollbackPath,
		Status:        "applying",
		AppliedEnv:    env,
		AppliedAt:     time.Now().UTC(),
		Checksum:      checksum,
	}
	if err := adapter.InsertStatus(ctx, table, entry); err != nil {
		return err
	}

	if err := adapter.ExecScript(ctx, forwardSQL); err != nil {
		entry.Status = "failed"
		entry.Error = sql.NullString{Valid: true, String: err.Error()}
		entry.AppliedAt = time.Now().UTC()
		_ = adapter.UpdateStatus(ctx, table, entry)

		if autoRollback && rollbackSQL != "" {
			rollbackErr := adapter.ExecScript(ctx, rollbackSQL)
			if rollbackErr != nil {
				entry.Status = "rollback_failed"
				entry.Error = sql.NullString{Valid: true, String: rollbackErr.Error()}
			} else {
				entry.Status = "rolled_back"
				entry.Error = sql.NullString{}
			}
			entry.AppliedAt = time.Now().UTC()
			_ = adapter.UpdateStatus(ctx, table, entry)
		}
		return err
	}

	entry.Status = "applied"
	entry.AppliedAt = time.Now().UTC()
	entry.Error = sql.NullString{}
	return adapter.UpdateStatus(ctx, table, entry)
}

func dbChecksum(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (r Runner) pickEnv(env string) (config.DBConfig, error) {
	switch env {
	case "staging":
		return r.Pair.Staging, nil
	case "production":
		return r.Pair.Production, nil
	default:
		return config.DBConfig{}, fmt.Errorf("unknown env %s", env)
	}
}
