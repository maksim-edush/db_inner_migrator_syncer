package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"db_inner_migrator_syncer/internal/secret"
)

var (
	ErrDBTargetNotFound  = errors.New("db target not found")
	ErrDBTargetInactive  = errors.New("db target is inactive")
	ErrDBTargetBadEngine = errors.New("invalid engine")
)

type DBTarget struct {
	ID        uuid.UUID       `json:"id"`
	DBSetID   uuid.UUID       `json:"db_set_id"`
	Engine    string          `json:"engine"`
	Host      string          `json:"host"`
	Port      int             `json:"port"`
	DBName    string          `json:"dbname"`
	Username  string          `json:"username"`
	Options   json.RawMessage `json:"options"`
	IsActive  bool            `json:"is_active"`
	CreatedAt time.Time       `json:"created_at"`
}

type CreateTargetInput struct {
	DBSetID  uuid.UUID
	Engine   string
	Host     string
	Port     int
	DBName   string
	Username string
	Password string
	Options  map[string]any
}

type UpdateTargetInput struct {
	Host     *string
	Port     *int
	DBName   *string
	Username *string
	Password *string
	Options  map[string]any
}

func CreateDBTarget(ctx context.Context, pool *pgxpool.Pool, key []byte, input CreateTargetInput) (*DBTarget, error) {
	if err := validateEngine(input.Engine); err != nil {
		return nil, err
	}
	if input.Port <= 0 {
		return nil, errors.New("port must be positive")
	}
	if strings.TrimSpace(input.Host) == "" || strings.TrimSpace(input.DBName) == "" || strings.TrimSpace(input.Username) == "" {
		return nil, errors.New("host, dbname, username required")
	}
	encPwd, err := secret.Encrypt(key, []byte(input.Password))
	if err != nil {
		return nil, err
	}

	id := uuid.New()
	options := json.RawMessage("{}")
	if input.Options != nil {
		body, err := json.Marshal(input.Options)
		if err != nil {
			return nil, fmt.Errorf("options marshal: %w", err)
		}
		options = body
	}

	if _, err := pool.Exec(ctx, `
INSERT INTO db_targets (id, db_set_id, engine, host, port, dbname, username, password_enc, options_json)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
`, id, input.DBSetID, strings.ToLower(input.Engine), input.Host, input.Port, input.DBName, input.Username, encPwd, options); err != nil {
		return nil, err
	}
	var createdAt time.Time
	if err := pool.QueryRow(ctx, `SELECT created_at FROM db_targets WHERE id = $1`, id).Scan(&createdAt); err != nil {
		return nil, err
	}

	return &DBTarget{
		ID:        id,
		DBSetID:   input.DBSetID,
		Engine:    strings.ToLower(input.Engine),
		Host:      input.Host,
		Port:      input.Port,
		DBName:    input.DBName,
		Username:  input.Username,
		Options:   options,
		IsActive:  true,
		CreatedAt: createdAt,
	}, nil
}

func ListDBTargetsBySet(ctx context.Context, pool *pgxpool.Pool, dbSetID uuid.UUID) ([]DBTarget, error) {
	rows, err := pool.Query(ctx, `
SELECT id, db_set_id, engine, host, port, dbname, username, options_json, is_active, created_at
FROM db_targets
WHERE db_set_id = $1
ORDER BY created_at DESC
`, dbSetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var targets []DBTarget
	for rows.Next() {
		var t DBTarget
		if err := rows.Scan(&t.ID, &t.DBSetID, &t.Engine, &t.Host, &t.Port, &t.DBName, &t.Username, &t.Options, &t.IsActive, &t.CreatedAt); err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

func GetDBTarget(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (*DBTarget, []byte, error) {
	var t DBTarget
	var encPwd []byte
	if err := pool.QueryRow(ctx, `
SELECT id, db_set_id, engine, host, port, dbname, username, password_enc, options_json, is_active, created_at
FROM db_targets
WHERE id = $1
`, id).Scan(&t.ID, &t.DBSetID, &t.Engine, &t.Host, &t.Port, &t.DBName, &t.Username, &encPwd, &t.Options, &t.IsActive, &t.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrDBTargetNotFound
		}
		return nil, nil, err
	}
	return &t, encPwd, nil
}

func DisableDBTarget(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) error {
	ct, err := pool.Exec(ctx, `UPDATE db_targets SET is_active = false WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrDBTargetNotFound
	}
	return nil
}

func UpdateDBTarget(ctx context.Context, pool *pgxpool.Pool, key []byte, id uuid.UUID, input UpdateTargetInput) (*DBTarget, error) {
	target, encPwd, err := GetDBTarget(ctx, pool, id)
	if err != nil {
		return nil, err
	}

	if input.Host != nil && strings.TrimSpace(*input.Host) != "" {
		target.Host = strings.TrimSpace(*input.Host)
	}
	if input.Port != nil && *input.Port > 0 {
		target.Port = *input.Port
	}
	if input.DBName != nil && strings.TrimSpace(*input.DBName) != "" {
		target.DBName = strings.TrimSpace(*input.DBName)
	}
	if input.Username != nil && strings.TrimSpace(*input.Username) != "" {
		target.Username = strings.TrimSpace(*input.Username)
	}

	newPassword := encPwd
	if input.Password != nil && strings.TrimSpace(*input.Password) != "" {
		enc, err := secret.Encrypt(key, []byte(*input.Password))
		if err != nil {
			return nil, err
		}
		newPassword = enc
	}

	options := target.Options
	if input.Options != nil {
		body, err := json.Marshal(input.Options)
		if err != nil {
			return nil, fmt.Errorf("options marshal: %w", err)
		}
		options = body
	}

	_, err = pool.Exec(ctx, `
UPDATE db_targets
SET host = $1, port = $2, dbname = $3, username = $4, password_enc = $5, options_json = $6
WHERE id = $7
`, target.Host, target.Port, target.DBName, target.Username, newPassword, options, id)
	if err != nil {
		return nil, err
	}

	target.Options = options
	return target, nil
}

func TestTargetConnection(ctx context.Context, pool *pgxpool.Pool, key []byte, targetID uuid.UUID) error {
	target, encPwd, err := GetDBTarget(ctx, pool, targetID)
	if err != nil {
		return err
	}
	if !target.IsActive {
		return ErrDBTargetInactive
	}
	password, err := secret.Decrypt(key, encPwd)
	if err != nil {
		return fmt.Errorf("decrypt password: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	switch strings.ToLower(target.Engine) {
	case "postgres":
		connStr := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", url.QueryEscape(target.Username), url.QueryEscape(string(password)), target.Host, target.Port, url.PathEscape(target.DBName))
		conn, err := pgx.Connect(ctx, connStr)
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		return conn.Ping(ctx)
	case "mysql":
		cfg := mysql.Config{
			User:                 target.Username,
			Passwd:               string(password),
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
		return db.PingContext(ctx)
	default:
		return ErrDBTargetBadEngine
	}
}

func validateEngine(engine string) error {
	engine = strings.ToLower(strings.TrimSpace(engine))
	if engine != "postgres" && engine != "mysql" {
		return ErrDBTargetBadEngine
	}
	return nil
}
