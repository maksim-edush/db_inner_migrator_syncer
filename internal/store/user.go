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

	"db_inner_migrator_syncer/internal/rbac"
)

var (
	ErrUserNotFound    = errors.New("user not found")
	ErrUserDisabled    = errors.New("user disabled")
	ErrUserEmailEmpty  = errors.New("email required")
	ErrUserNameEmpty   = errors.New("name required")
	ErrUserRoleInvalid = errors.New("invalid role")
	ErrUserEmailExists = errors.New("email already exists")
)

type User struct {
	ID          uuid.UUID
	Email       string
	Name        string
	Role        rbac.Role
	Provider    string
	GoogleSub   *string
	IsDisabled  bool
	LastLoginAt *time.Time
}

type UserRecord struct {
	ID          uuid.UUID
	Email       string
	Name        string
	Role        rbac.Role
	Provider    string
	GoogleSub   *string
	IsDisabled  bool
	LastLoginAt *time.Time
	CreatedAt   time.Time
}

type CreateUserInput struct {
	Email string
	Name  string
	Role  rbac.Role
}

type UpdateUserInput struct {
	Name string
	Role rbac.Role
}

func GetUserByID(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (*User, error) {
	row := pool.QueryRow(ctx, `
SELECT id, email, name, role, provider, google_sub, is_disabled, last_login_at
FROM users
WHERE id = $1
`, id)
	return scanUser(row)
}

func FindOrCreateGoogleUser(ctx context.Context, pool *pgxpool.Pool, sub, email, name string, allowAutoProvision bool) (*User, error) {
	email = strings.ToLower(email)
	// 1) Prefer google_sub match
	if user, err := findGoogleUser(ctx, pool, sub); err == nil {
		return user, updateLastLogin(ctx, pool, user.ID)
	} else if !errors.Is(err, ErrUserNotFound) {
		return nil, err
	}

	// 2) Fallback to email match (pre-provisioned)
	if user, err := findUserByEmail(ctx, pool, email); err == nil {
		if user.GoogleSub == nil || *user.GoogleSub == "" {
			if err := linkGoogleSub(ctx, pool, user.ID, sub); err != nil {
				return nil, err
			}
			user.GoogleSub = &sub
		}
		return user, updateLastLogin(ctx, pool, user.ID)
	} else if !errors.Is(err, ErrUserNotFound) {
		return nil, err
	}

	// 3) Auto-provision
	if !allowAutoProvision {
		return nil, ErrUserNotFound
	}

	id := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO users (id, email, name, role, provider, google_sub, is_disabled, created_at)
VALUES ($1, $2, $3, $4, 'google', $5, false, now())
`, id, email, name, rbac.RoleUser, sub); err != nil {
		return nil, err
	}
	user := &User{
		ID:        id,
		Email:     email,
		Name:      name,
		Role:      rbac.RoleUser,
		Provider:  "google",
		GoogleSub: &sub,
	}
	return user, updateLastLogin(ctx, pool, user.ID)
}

func findGoogleUser(ctx context.Context, pool *pgxpool.Pool, sub string) (*User, error) {
	row := pool.QueryRow(ctx, `
SELECT id, email, name, role, provider, google_sub, is_disabled, last_login_at
FROM users
WHERE google_sub = $1
`, sub)
	return scanUser(row)
}

func findUserByEmail(ctx context.Context, pool *pgxpool.Pool, email string) (*User, error) {
	row := pool.QueryRow(ctx, `
SELECT id, email, name, role, provider, google_sub, is_disabled, last_login_at
FROM users
WHERE email = $1
`, email)
	return scanUser(row)
}

func linkGoogleSub(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, sub string) error {
	_, err := pool.Exec(ctx, `UPDATE users SET google_sub = $1 WHERE id = $2`, sub, id)
	return err
}

func updateLastLogin(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) error {
	_, err := pool.Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, id)
	return err
}

func scanUser(row pgx.Row) (*User, error) {
	var user User
	if err := row.Scan(&user.ID, &user.Email, &user.Name, &user.Role, &user.Provider, &user.GoogleSub, &user.IsDisabled, &user.LastLoginAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	if user.IsDisabled {
		return nil, ErrUserDisabled
	}
	return &user, nil
}

func ListUsers(ctx context.Context, pool *pgxpool.Pool) ([]UserRecord, error) {
	rows, err := pool.Query(ctx, `
SELECT id, email, name, role, provider, google_sub, is_disabled, last_login_at, created_at
FROM users
ORDER BY email
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []UserRecord
	for rows.Next() {
		var user UserRecord
		if err := rows.Scan(&user.ID, &user.Email, &user.Name, &user.Role, &user.Provider, &user.GoogleSub, &user.IsDisabled, &user.LastLoginAt, &user.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func CreateUser(ctx context.Context, pool *pgxpool.Pool, input CreateUserInput) (*UserRecord, error) {
	email := strings.ToLower(strings.TrimSpace(input.Email))
	if email == "" {
		return nil, ErrUserEmailEmpty
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, ErrUserNameEmpty
	}
	role := input.Role
	if role == "" {
		role = rbac.RoleUser
	}
	if !validRole(role) {
		return nil, ErrUserRoleInvalid
	}

	id := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO users (id, email, name, role, provider, is_disabled, created_at)
VALUES ($1, $2, $3, $4, 'google', false, now())
`, id, email, name, role); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrUserEmailExists
		}
		return nil, err
	}
	return GetUserRecordByID(ctx, pool, id)
}

func UpdateUser(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, input UpdateUserInput) (*UserRecord, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, ErrUserNameEmpty
	}
	if !validRole(input.Role) {
		return nil, ErrUserRoleInvalid
	}
	ct, err := pool.Exec(ctx, `UPDATE users SET name = $1, role = $2 WHERE id = $3`, name, input.Role, id)
	if err != nil {
		return nil, err
	}
	if ct.RowsAffected() == 0 {
		return nil, ErrUserNotFound
	}
	return GetUserRecordByID(ctx, pool, id)
}

func DisableUser(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) error {
	ct, err := pool.Exec(ctx, `UPDATE users SET is_disabled = true WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}

func GetUserRecordByID(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (*UserRecord, error) {
	row := pool.QueryRow(ctx, `
SELECT id, email, name, role, provider, google_sub, is_disabled, last_login_at, created_at
FROM users
WHERE id = $1
`, id)
	return scanUserRecord(row)
}

func scanUserRecord(row pgx.Row) (*UserRecord, error) {
	var user UserRecord
	if err := row.Scan(&user.ID, &user.Email, &user.Name, &user.Role, &user.Provider, &user.GoogleSub, &user.IsDisabled, &user.LastLoginAt, &user.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return &user, nil
}

func validRole(role rbac.Role) bool {
	switch role {
	case rbac.RoleUser, rbac.RoleManager, rbac.RoleAdmin:
		return true
	default:
		return false
	}
}
