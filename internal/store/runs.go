package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrRunNotFound        = errors.New("run not found")
	ErrRunInvalidStatus   = errors.New("run is not awaiting approval")
	ErrRunNoTargets       = errors.New("no active targets in db set")
	ErrRunEnvInvalid      = errors.New("invalid env")
	ErrChecksumMismatch   = errors.New("migration checksum changed; request new approval")
	ErrAlreadyApplied     = errors.New("migration already applied with same checksum")
	ErrRollbackMissingSQL = errors.New("sql_down is required for rollback")
)

type Run struct {
	ID                    uuid.UUID  `json:"id"`
	RunType               string     `json:"run_type"`
	MigrationID           uuid.UUID  `json:"migration_id"`
	ProjectID             uuid.UUID  `json:"project_id"`
	Env                   string     `json:"env"`
	DBSetID               uuid.UUID  `json:"db_set_id"`
	Status                string     `json:"status"`
	RequestedBy           uuid.UUID  `json:"requested_by"`
	RequestedAt           time.Time  `json:"requested_at"`
	ApprovedBy            *uuid.UUID `json:"approved_by,omitempty"`
	ApprovedAt            *time.Time `json:"approved_at,omitempty"`
	ApprovalComment       *string    `json:"approval_comment,omitempty"`
	ExecutedBy            *uuid.UUID `json:"executed_by,omitempty"`
	StartedAt             *time.Time `json:"started_at,omitempty"`
	FinishedAt            *time.Time `json:"finished_at,omitempty"`
	ChecksumUpAtRequest   string     `json:"checksum_up_at_request"`
	ChecksumDownAtRequest *string    `json:"checksum_down_at_request,omitempty"`
}

type RunItem struct {
	ID         uuid.UUID  `json:"id"`
	RunID      uuid.UUID  `json:"run_id"`
	DBTargetID uuid.UUID  `json:"db_target_id"`
	Status     string     `json:"status"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      *string    `json:"error,omitempty"`
	Log        *string    `json:"log,omitempty"`
}

type RunWithItems struct {
	Run
	Items []RunItem `json:"items"`
}

type RequestRunInput struct {
	ProjectID   uuid.UUID
	MigrationID uuid.UUID
	DBSetID     uuid.UUID
	Env         string
	RequestedBy uuid.UUID
	RunType     string
}

type ApprovalDecisionInput struct {
	RunID     uuid.UUID
	ProjectID uuid.UUID
	ActorID   uuid.UUID
	Comment   string
	Decision  string // approved or denied
}

func RequestRun(ctx context.Context, pool *pgxpool.Pool, input RequestRunInput) (*RunWithItems, error) {
	env := strings.ToLower(strings.TrimSpace(input.Env))
	if env != "daily" && env != "stg" && env != "prd" {
		return nil, ErrRunEnvInvalid
	}
	runType := strings.ToLower(strings.TrimSpace(input.RunType))
	if runType == "" {
		runType = "apply"
	}
	if runType != "apply" && runType != "rollback" {
		return nil, errors.New("invalid run type")
	}

	mig, err := GetMigration(ctx, pool, input.ProjectID, input.MigrationID)
	if err != nil {
		return nil, err
	}
	if runType == "rollback" && (mig.SQLDown == nil || strings.TrimSpace(*mig.SQLDown) == "") {
		return nil, ErrRollbackMissingSQL
	}

	set, err := GetDBSet(ctx, pool, input.DBSetID)
	if err != nil {
		return nil, err
	}
	if set.ProjectID != input.ProjectID {
		return nil, ErrDBSetNotFound
	}
	if set.Env != env {
		return nil, errors.New("db set env mismatch")
	}

	targets, err := ListDBTargetsBySet(ctx, pool, input.DBSetID)
	if err != nil {
		return nil, err
	}
	activeTargets := make([]DBTarget, 0, len(targets))
	for _, t := range targets {
		if t.IsActive {
			activeTargets = append(activeTargets, t)
		}
	}
	if len(activeTargets) == 0 {
		return nil, ErrRunNoTargets
	}

	runID := uuid.New()
	now := time.Now().UTC()
	run := Run{
		ID:                    runID,
		RunType:               runType,
		MigrationID:           mig.ID,
		ProjectID:             input.ProjectID,
		Env:                   env,
		DBSetID:               input.DBSetID,
		Status:                "awaiting_approval",
		RequestedBy:           input.RequestedBy,
		RequestedAt:           now,
		ChecksumUpAtRequest:   mig.ChecksumUp,
		ChecksumDownAtRequest: mig.ChecksumDown,
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) // nolint:errcheck

	if _, err := tx.Exec(ctx, `
INSERT INTO runs (id, run_type, migration_id, project_id, env, db_set_id, status, requested_by, requested_at, checksum_up_at_request, checksum_down_at_request)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
`, run.ID, run.RunType, run.MigrationID, run.ProjectID, run.Env, run.DBSetID, run.Status, run.RequestedBy, run.RequestedAt, run.ChecksumUpAtRequest, run.ChecksumDownAtRequest); err != nil {
		return nil, err
	}

	var items []RunItem
	for _, t := range activeTargets {
		item := RunItem{
			ID:         uuid.New(),
			RunID:      run.ID,
			DBTargetID: t.ID,
			Status:     "queued",
		}
		items = append(items, item)
		if _, err := tx.Exec(ctx, `
INSERT INTO run_items (id, run_id, db_target_id, status)
VALUES ($1, $2, $3, $4)
`, item.ID, item.RunID, item.DBTargetID, item.Status); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return &RunWithItems{Run: run, Items: items}, nil
}

func ApproveRun(ctx context.Context, pool *pgxpool.Pool, input ApprovalDecisionInput) (*Run, error) {
	run, err := getRun(ctx, pool, input.RunID, input.ProjectID)
	if err != nil {
		return nil, err
	}
	if run.Status != "awaiting_approval" {
		return nil, ErrRunInvalidStatus
	}

	mig, err := GetMigration(ctx, pool, run.ProjectID, run.MigrationID)
	if err != nil {
		return nil, err
	}
	if mig.ChecksumUp != run.ChecksumUpAtRequest || !equalNullable(mig.ChecksumDown, run.ChecksumDownAtRequest) {
		return nil, ErrChecksumMismatch
	}

	now := time.Now().UTC()
	comment := strings.TrimSpace(input.Comment)

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) // nolint:errcheck

	if _, err := tx.Exec(ctx, `
UPDATE runs
SET status = 'approved', approved_by = $1, approved_at = $2, approval_comment = $3
WHERE id = $4
`, input.ActorID, now, nullableString(comment), input.RunID); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `
INSERT INTO approvals (id, migration_id, env, decision, comment, decided_by, decided_at, checksum_up, checksum_down)
VALUES ($1, $2, $3, 'approved', $4, $5, $6, $7, $8)
`, uuid.New(), run.MigrationID, run.Env, nullableString(comment), input.ActorID, now, run.ChecksumUpAtRequest, run.ChecksumDownAtRequest); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	run.Status = "approved"
	run.ApprovedBy = &input.ActorID
	run.ApprovedAt = &now
	run.ApprovalComment = nullableString(comment)
	return run, nil
}

func DenyRun(ctx context.Context, pool *pgxpool.Pool, input ApprovalDecisionInput) (*Run, error) {
	run, err := getRun(ctx, pool, input.RunID, input.ProjectID)
	if err != nil {
		return nil, err
	}
	if run.Status != "awaiting_approval" {
		return nil, ErrRunInvalidStatus
	}

	now := time.Now().UTC()
	comment := strings.TrimSpace(input.Comment)

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) // nolint:errcheck

	if _, err := tx.Exec(ctx, `
UPDATE runs
SET status = 'denied', approved_by = $1, approved_at = $2, approval_comment = $3
WHERE id = $4
`, input.ActorID, now, nullableString(comment), input.RunID); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `
INSERT INTO approvals (id, migration_id, env, decision, comment, decided_by, decided_at, checksum_up, checksum_down)
VALUES ($1, $2, $3, 'denied', $4, $5, $6, $7, $8)
`, uuid.New(), run.MigrationID, run.Env, nullableString(comment), input.ActorID, now, run.ChecksumUpAtRequest, run.ChecksumDownAtRequest); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	run.Status = "denied"
	run.ApprovedBy = &input.ActorID
	run.ApprovedAt = &now
	run.ApprovalComment = nullableString(comment)
	return run, nil
}

func GetRunWithItems(ctx context.Context, pool *pgxpool.Pool, projectID uuid.UUID, runID uuid.UUID) (*RunWithItems, error) {
	run, err := getRun(ctx, pool, runID, projectID)
	if err != nil {
		return nil, err
	}
	items, err := listRunItems(ctx, pool, run.ID)
	if err != nil {
		return nil, err
	}
	return &RunWithItems{Run: *run, Items: items}, nil
}

func ListRunsForMigration(ctx context.Context, pool *pgxpool.Pool, projectID uuid.UUID, migrationID uuid.UUID) ([]Run, error) {
	rows, err := pool.Query(ctx, `
SELECT id, run_type, migration_id, project_id, env, db_set_id, status, requested_by, requested_at, approved_by, approved_at, approval_comment, executed_by, started_at, finished_at, checksum_up_at_request, checksum_down_at_request
FROM runs
WHERE project_id = $1 AND migration_id = $2
ORDER BY requested_at DESC
`, projectID, migrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []Run
	for rows.Next() {
		var run Run
		if err := rows.Scan(&run.ID, &run.RunType, &run.MigrationID, &run.ProjectID, &run.Env, &run.DBSetID, &run.Status, &run.RequestedBy, &run.RequestedAt, &run.ApprovedBy, &run.ApprovedAt, &run.ApprovalComment, &run.ExecutedBy, &run.StartedAt, &run.FinishedAt, &run.ChecksumUpAtRequest, &run.ChecksumDownAtRequest); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func getRun(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID, projectID uuid.UUID) (*Run, error) {
	var run Run
	if err := pool.QueryRow(ctx, `
SELECT id, run_type, migration_id, project_id, env, db_set_id, status, requested_by, requested_at, approved_by, approved_at, approval_comment, executed_by, started_at, finished_at, checksum_up_at_request, checksum_down_at_request
FROM runs
WHERE id = $1 AND project_id = $2
`, runID, projectID).Scan(
		&run.ID, &run.RunType, &run.MigrationID, &run.ProjectID, &run.Env, &run.DBSetID, &run.Status, &run.RequestedBy, &run.RequestedAt, &run.ApprovedBy, &run.ApprovedAt, &run.ApprovalComment, &run.ExecutedBy, &run.StartedAt, &run.FinishedAt, &run.ChecksumUpAtRequest, &run.ChecksumDownAtRequest,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, err
	}
	return &run, nil
}

func listRunItems(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID) ([]RunItem, error) {
	rows, err := pool.Query(ctx, `
SELECT id, run_id, db_target_id, status, started_at, finished_at, error, log
FROM run_items
WHERE run_id = $1
ORDER BY id
`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []RunItem
	for rows.Next() {
		var it RunItem
		if err := rows.Scan(&it.ID, &it.RunID, &it.DBTargetID, &it.Status, &it.StartedAt, &it.FinishedAt, &it.Error, &it.Log); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func nullableString(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	val := strings.TrimSpace(s)
	return &val
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
