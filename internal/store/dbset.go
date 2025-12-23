package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrDBSetNotFound  = errors.New("db set not found")
	ErrDBSetNameEmpty = errors.New("db set name required")
	ErrEnvInvalid     = errors.New("invalid env")
)

type DBSet struct {
	ID        uuid.UUID `json:"id"`
	ProjectID uuid.UUID `json:"project_id"`
	Env       string    `json:"env"`
	Name      string    `json:"name"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
}

func CreateDBSet(ctx context.Context, pool *pgxpool.Pool, projectID uuid.UUID, env string, name string, createdBy uuid.UUID) (*DBSet, error) {
	env = strings.ToLower(strings.TrimSpace(env))
	if env != "daily" && env != "stg" && env != "prd" {
		return nil, ErrEnvInvalid
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrDBSetNameEmpty
	}
	id := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO db_sets (id, project_id, env, name, created_by)
VALUES ($1, $2, $3, $4, $5)
`, id, projectID, env, name, createdBy); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, errors.New("db set name already exists for project/env")
		}
		return nil, err
	}

	var createdAt time.Time
	if err := pool.QueryRow(ctx, `SELECT created_at FROM db_sets WHERE id = $1`, id).Scan(&createdAt); err != nil {
		return nil, err
	}
	return &DBSet{
		ID:        id,
		ProjectID: projectID,
		Env:       env,
		Name:      name,
		IsActive:  true,
		CreatedAt: createdAt,
	}, nil
}

func ListDBSets(ctx context.Context, pool *pgxpool.Pool, projectID uuid.UUID, envFilter string) ([]DBSet, error) {
	var rows pgx.Rows
	var err error
	if envFilter != "" {
		envFilter = strings.ToLower(envFilter)
		rows, err = pool.Query(ctx, `
SELECT id, project_id, env, name, is_active, created_at
FROM db_sets
WHERE project_id = $1 AND env = $2
ORDER BY name
`, projectID, envFilter)
	} else {
		rows, err = pool.Query(ctx, `
SELECT id, project_id, env, name, is_active, created_at
FROM db_sets
WHERE project_id = $1
ORDER BY name
`, projectID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sets []DBSet
	for rows.Next() {
		var s DBSet
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.Env, &s.Name, &s.IsActive, &s.CreatedAt); err != nil {
			return nil, err
		}
		sets = append(sets, s)
	}
	return sets, rows.Err()
}

func GetDBSet(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (*DBSet, error) {
	var s DBSet
	if err := pool.QueryRow(ctx, `
SELECT id, project_id, env, name, is_active, created_at
FROM db_sets
WHERE id = $1
`, id).Scan(&s.ID, &s.ProjectID, &s.Env, &s.Name, &s.IsActive, &s.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDBSetNotFound
		}
		return nil, err
	}
	return &s, nil
}

func DisableDBSet(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) error {
	ct, err := pool.Exec(ctx, `UPDATE db_sets SET is_active = false WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrDBSetNotFound
	}
	return nil
}
