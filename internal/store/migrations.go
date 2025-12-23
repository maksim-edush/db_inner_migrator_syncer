package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrMigrationNotFound   = errors.New("migration not found")
	ErrMigrationKeyEmpty   = errors.New("migration key required")
	ErrMigrationNameEmpty  = errors.New("migration name required")
	ErrMigrationSQLMissing = errors.New("migration sql_up required")
	ErrTxModeInvalid       = errors.New("invalid transaction_mode")
)

type Migration struct {
	ID              uuid.UUID `json:"id"`
	ProjectID       uuid.UUID `json:"project_id"`
	Key             string    `json:"key"`
	Name            string    `json:"name"`
	Jira            string    `json:"jira"`
	Description     string    `json:"description"`
	SQLUp           string    `json:"sql_up"`
	SQLDown         *string   `json:"sql_down,omitempty"`
	ChecksumUp      string    `json:"checksum_up"`
	ChecksumDown    *string   `json:"checksum_down,omitempty"`
	Version         int       `json:"version"`
	TransactionMode string    `json:"transaction_mode"`
	CreatedBy       uuid.UUID `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type CreateMigrationInput struct {
	ProjectID       uuid.UUID
	Key             string
	Name            string
	Jira            string
	Description     string
	SQLUp           string
	SQLDown         *string
	TransactionMode string
	CreatedBy       uuid.UUID
}

type UpdateMigrationInput struct {
	Name            *string `json:"name"`
	Jira            *string `json:"jira"`
	Description     *string `json:"description"`
	SQLUp           *string `json:"sql_up"`
	SQLDown         *string `json:"sql_down"`
	TransactionMode *string `json:"transaction_mode"`
}

func CreateMigration(ctx context.Context, pool *pgxpool.Pool, input CreateMigrationInput) (*Migration, error) {
	if strings.TrimSpace(input.Key) == "" {
		return nil, ErrMigrationKeyEmpty
	}
	if strings.TrimSpace(input.Name) == "" {
		return nil, ErrMigrationNameEmpty
	}
	if strings.TrimSpace(input.SQLUp) == "" {
		return nil, ErrMigrationSQLMissing
	}
	mode := normalizeTxMode(input.TransactionMode)
	if mode == "" {
		return nil, ErrTxModeInvalid
	}

	now := time.Now().UTC()
	id := uuid.New()
	checksumUp := checksum(input.SQLUp)
	var checksumDown *string
	if input.SQLDown != nil {
		down := checksum(*input.SQLDown)
		checksumDown = &down
	}

	_, err := pool.Exec(ctx, `
INSERT INTO migrations (id, project_id, migration_key, name, jira, description, sql_up, sql_down, checksum_up, checksum_down, version, transaction_mode, created_by, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 1, $11, $12, $13, $13)
`, id, input.ProjectID, input.Key, input.Name, input.Jira, input.Description, input.SQLUp, input.SQLDown, checksumUp, checksumDown, mode, input.CreatedBy, now)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, errors.New("migration key already exists")
		}
		return nil, err
	}

	return &Migration{
		ID:              id,
		ProjectID:       input.ProjectID,
		Key:             input.Key,
		Name:            input.Name,
		Jira:            input.Jira,
		Description:     input.Description,
		SQLUp:           input.SQLUp,
		SQLDown:         input.SQLDown,
		ChecksumUp:      checksumUp,
		ChecksumDown:    checksumDown,
		Version:         1,
		TransactionMode: mode,
		CreatedBy:       input.CreatedBy,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

func GetMigration(ctx context.Context, pool *pgxpool.Pool, projectID uuid.UUID, id uuid.UUID) (*Migration, error) {
	var m Migration
	if err := pool.QueryRow(ctx, `
SELECT id, project_id, migration_key, name, jira, description, sql_up, sql_down, checksum_up, checksum_down, version, transaction_mode, created_by, created_at, updated_at
FROM migrations
WHERE id = $1 AND project_id = $2
`, id, projectID).Scan(&m.ID, &m.ProjectID, &m.Key, &m.Name, &m.Jira, &m.Description, &m.SQLUp, &m.SQLDown, &m.ChecksumUp, &m.ChecksumDown, &m.Version, &m.TransactionMode, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMigrationNotFound
		}
		return nil, err
	}
	return &m, nil
}

func ListMigrations(ctx context.Context, pool *pgxpool.Pool, projectID uuid.UUID, search string) ([]Migration, error) {
	var rows pgx.Rows
	var err error
	if search != "" {
		pattern := "%" + strings.ToLower(search) + "%"
		rows, err = pool.Query(ctx, `
SELECT id, project_id, migration_key, name, jira, description, sql_up, sql_down, checksum_up, checksum_down, version, transaction_mode, created_by, created_at, updated_at
FROM migrations
WHERE project_id = $1 AND (LOWER(migration_key) LIKE $2 OR LOWER(name) LIKE $2 OR LOWER(jira) LIKE $2)
ORDER BY created_at DESC
`, projectID, pattern)
	} else {
		rows, err = pool.Query(ctx, `
SELECT id, project_id, migration_key, name, jira, description, sql_up, sql_down, checksum_up, checksum_down, version, transaction_mode, created_by, created_at, updated_at
FROM migrations
WHERE project_id = $1
ORDER BY created_at DESC
`, projectID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Migration
	for rows.Next() {
		var m Migration
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.Key, &m.Name, &m.Jira, &m.Description, &m.SQLUp, &m.SQLDown, &m.ChecksumUp, &m.ChecksumDown, &m.Version, &m.TransactionMode, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

// UpdateMigration updates fields; if SQL changes, increments version and returns sqlChanged=true.
func UpdateMigration(ctx context.Context, pool *pgxpool.Pool, projectID uuid.UUID, id uuid.UUID, input UpdateMigrationInput) (*Migration, bool, error) {
	current, err := GetMigration(ctx, pool, projectID, id)
	if err != nil {
		return nil, false, err
	}

	name := coalesceString(input.Name, current.Name)
	jira := coalesceString(input.Jira, current.Jira)
	description := coalesceString(input.Description, current.Description)
	sqlUp := coalesceString(input.SQLUp, current.SQLUp)

	var sqlDown *string
	if input.SQLDown != nil {
		down := strings.TrimSpace(*input.SQLDown)
		if down == "" {
			sqlDown = nil
		} else {
			sqlDown = &down
		}
	} else {
		sqlDown = current.SQLDown
	}

	txMode := current.TransactionMode
	if input.TransactionMode != nil {
		mode := normalizeTxMode(*input.TransactionMode)
		if mode == "" {
			return nil, false, ErrTxModeInvalid
		}
		txMode = mode
	}

	if strings.TrimSpace(name) == "" {
		return nil, false, ErrMigrationNameEmpty
	}
	if strings.TrimSpace(sqlUp) == "" {
		return nil, false, ErrMigrationSQLMissing
	}

	sqlChanged := sqlUp != current.SQLUp
	if (sqlDown == nil && current.SQLDown != nil) || (sqlDown != nil && current.SQLDown == nil) {
		sqlChanged = true
	}
	if sqlDown != nil && current.SQLDown != nil && *sqlDown != *current.SQLDown {
		sqlChanged = true
	}

	version := current.Version
	checksumUp := current.ChecksumUp
	checksumDown := current.ChecksumDown
	if sqlChanged {
		version++
		checksumUp = checksum(sqlUp)
		if sqlDown != nil {
			down := checksum(*sqlDown)
			checksumDown = &down
		} else {
			checksumDown = nil
		}
	}

	now := time.Now().UTC()
	_, err = pool.Exec(ctx, `
UPDATE migrations
SET name = $1, jira = $2, description = $3, sql_up = $4, sql_down = $5,
    checksum_up = $6, checksum_down = $7, version = $8, transaction_mode = $9,
    updated_at = $10
WHERE id = $11 AND project_id = $12
`, name, jira, description, sqlUp, sqlDown, checksumUp, checksumDown, version, txMode, now, id, projectID)
	if err != nil {
		return nil, sqlChanged, err
	}

	current.Name = name
	current.Jira = jira
	current.Description = description
	current.SQLUp = sqlUp
	current.SQLDown = sqlDown
	current.ChecksumUp = checksumUp
	current.ChecksumDown = checksumDown
	current.Version = version
	current.TransactionMode = txMode
	current.UpdatedAt = now

	return current, sqlChanged, nil
}

func DeleteApprovalsForMigration(ctx context.Context, pool *pgxpool.Pool, migrationID uuid.UUID) error {
	_, err := pool.Exec(ctx, `DELETE FROM approvals WHERE migration_id = $1`, migrationID)
	return err
}

func checksum(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return hex.EncodeToString(sum[:])
}

func normalizeTxMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "auto":
		return "auto"
	case "single_transaction":
		return "single_transaction"
	case "no_transaction":
		return "no_transaction"
	default:
		return ""
	}
}

func coalesceString(ptr *string, current string) string {
	if ptr == nil {
		return current
	}
	return *ptr
}
