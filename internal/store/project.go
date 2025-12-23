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
	ErrProjectNotFound   = errors.New("project not found")
	ErrProjectNameEmpty  = errors.New("project name required")
	ErrProjectNameExists = errors.New("project name already exists")
)

type Project struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func ListProjects(ctx context.Context, pool *pgxpool.Pool) ([]Project, error) {
	rows, err := pool.Query(ctx, `SELECT id, name, created_at FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedAt); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func CreateProject(ctx context.Context, pool *pgxpool.Pool, name string) (*Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrProjectNameEmpty
	}
	id := uuid.New()
	var createdAt time.Time
	if _, err := pool.Exec(ctx, `INSERT INTO projects (id, name) VALUES ($1, $2)`, id, name); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrProjectNameExists
		}
		return nil, err
	}
	if err := pool.QueryRow(ctx, `SELECT created_at FROM projects WHERE id = $1`, id).Scan(&createdAt); err != nil {
		return nil, err
	}
	return &Project{ID: id, Name: name, CreatedAt: createdAt}, nil
}

func GetProject(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (*Project, error) {
	var p Project
	if err := pool.QueryRow(ctx, `SELECT id, name, created_at FROM projects WHERE id = $1`, id).Scan(&p.ID, &p.Name, &p.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrProjectNotFound
		}
		return nil, err
	}
	return &p, nil
}
