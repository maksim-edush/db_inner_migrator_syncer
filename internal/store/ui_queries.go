package store

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RunSummary struct {
	ID           uuid.UUID `json:"id"`
	RunType      string    `json:"run_type"`
	Env          string    `json:"env"`
	Status       string    `json:"status"`
	RequestedAt  time.Time `json:"requested_at"`
	ProjectName  string    `json:"project_name"`
	MigrationKey string    `json:"migration_key"`
	RequestedBy  string    `json:"requested_by"`
}

type RunListFilter struct {
	Env          string
	Status       string
	MigrationKey string
}

type LatestRunItem struct {
	TargetID    uuid.UUID
	MigrationID uuid.UUID
	RunID       uuid.UUID
	RunType     string
	RunStatus   string
	ItemStatus  string
	RequestedAt time.Time
	FinishedAt  *time.Time
}

func CountRunsByStatus(ctx context.Context, pool *pgxpool.Pool, projectID uuid.UUID, status string) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `
SELECT COUNT(1) FROM runs WHERE project_id = $1 AND status = $2
`, projectID, status).Scan(&count)
	return count, err
}

func CountFailedRunsSince(ctx context.Context, pool *pgxpool.Pool, projectID uuid.UUID, since time.Time) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `
SELECT COUNT(1) FROM runs WHERE project_id = $1 AND status = 'failed' AND finished_at >= $2
`, projectID, since).Scan(&count)
	return count, err
}

func ListRecentRuns(ctx context.Context, pool *pgxpool.Pool, projectID uuid.UUID, limit int) ([]RunSummary, error) {
	rows, err := pool.Query(ctx, `
SELECT r.id, r.run_type, r.env, r.status, r.requested_at, p.name, m.migration_key, u.email
FROM runs r
JOIN migrations m ON r.migration_id = m.id
JOIN projects p ON r.project_id = p.id
LEFT JOIN users u ON r.requested_by = u.id
WHERE r.project_id = $1
ORDER BY r.requested_at DESC
LIMIT $2
`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []RunSummary
	for rows.Next() {
		var item RunSummary
		if err := rows.Scan(&item.ID, &item.RunType, &item.Env, &item.Status, &item.RequestedAt, &item.ProjectName, &item.MigrationKey, &item.RequestedBy); err != nil {
			return nil, err
		}
		list = append(list, item)
	}
	return list, rows.Err()
}

func ListRuns(ctx context.Context, pool *pgxpool.Pool, projectID uuid.UUID, filter RunListFilter, limit int) ([]RunSummary, error) {
	query := `
SELECT r.id, r.run_type, r.env, r.status, r.requested_at, p.name, m.migration_key, u.email
FROM runs r
JOIN migrations m ON r.migration_id = m.id
JOIN projects p ON r.project_id = p.id
LEFT JOIN users u ON r.requested_by = u.id
WHERE r.project_id = $1
`
	args := []any{projectID}
	argIdx := 2
	if filter.Env != "" {
		query += " AND r.env = $" + itoa(argIdx)
		args = append(args, filter.Env)
		argIdx++
	}
	if filter.Status != "" {
		query += " AND r.status = $" + itoa(argIdx)
		args = append(args, filter.Status)
		argIdx++
	}
	if filter.MigrationKey != "" {
		query += " AND LOWER(m.migration_key) LIKE $" + itoa(argIdx)
		args = append(args, "%"+strings.ToLower(filter.MigrationKey)+"%")
		argIdx++
	}
	query += " ORDER BY r.requested_at DESC"
	if limit > 0 {
		query += " LIMIT $" + itoa(argIdx)
		args = append(args, limit)
	}

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []RunSummary
	for rows.Next() {
		var item RunSummary
		if err := rows.Scan(&item.ID, &item.RunType, &item.Env, &item.Status, &item.RequestedAt, &item.ProjectName, &item.MigrationKey, &item.RequestedBy); err != nil {
			return nil, err
		}
		list = append(list, item)
	}
	return list, rows.Err()
}

func ListPendingApprovals(ctx context.Context, pool *pgxpool.Pool, projectID uuid.UUID, envFilter string) ([]RunSummary, error) {
	query := `
SELECT r.id, r.run_type, r.env, r.status, r.requested_at, p.name, m.migration_key, u.email
FROM runs r
JOIN migrations m ON r.migration_id = m.id
JOIN projects p ON r.project_id = p.id
LEFT JOIN users u ON r.requested_by = u.id
WHERE r.project_id = $1 AND r.status = 'awaiting_approval'
`
	args := []any{projectID}
	if envFilter != "" {
		query += " AND r.env = $2"
		args = append(args, envFilter)
	}
	query += " ORDER BY r.requested_at DESC"

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []RunSummary
	for rows.Next() {
		var item RunSummary
		if err := rows.Scan(&item.ID, &item.RunType, &item.Env, &item.Status, &item.RequestedAt, &item.ProjectName, &item.MigrationKey, &item.RequestedBy); err != nil {
			return nil, err
		}
		list = append(list, item)
	}
	return list, rows.Err()
}

type TimelineEvent struct {
	Time       time.Time
	ActorEmail string
	Action     string
	Payload    string
}

func ListTimelineEvents(ctx context.Context, pool *pgxpool.Pool, migrationID uuid.UUID) ([]TimelineEvent, error) {
	rows, err := pool.Query(ctx, `
SELECT ae.created_at, COALESCE(u.email, ''), ae.action, ae.payload
FROM audit_events ae
LEFT JOIN users u ON ae.actor_id = u.id
WHERE (ae.entity_type = 'migration' AND ae.entity_id = $1)
   OR (ae.entity_type = 'run' AND ae.entity_id IN (SELECT id FROM runs WHERE migration_id = $1))
ORDER BY ae.created_at DESC
LIMIT 200
`, migrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []TimelineEvent
	for rows.Next() {
		var ev TimelineEvent
		var raw []byte
		if err := rows.Scan(&ev.Time, &ev.ActorEmail, &ev.Action, &raw); err != nil {
			return nil, err
		}
		ev.Payload = formatPayload(raw)
		events = append(events, ev)
	}
	return events, rows.Err()
}

func formatPayload(raw []byte) string {
	if len(raw) == 0 {
		return "{}"
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err == nil {
		if pretty, err := json.MarshalIndent(decoded, "", "  "); err == nil {
			return string(pretty)
		}
	}
	return string(raw)
}

func GetRunItemLog(ctx context.Context, pool *pgxpool.Pool, projectID uuid.UUID, runID uuid.UUID, itemID uuid.UUID) (*RunItem, error) {
	var item RunItem
	err := pool.QueryRow(ctx, `
SELECT ri.id, ri.run_id, ri.db_target_id, ri.status, ri.started_at, ri.finished_at, ri.error, ri.log
FROM run_items ri
JOIN runs r ON ri.run_id = r.id
WHERE ri.id = $1 AND r.id = $2 AND r.project_id = $3
`, itemID, runID, projectID).Scan(&item.ID, &item.RunID, &item.DBTargetID, &item.Status, &item.StartedAt, &item.FinishedAt, &item.Error, &item.Log)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrRunNotFound
		}
		return nil, err
	}
	return &item, nil
}

func LatestRunsByMigrationEnv(ctx context.Context, pool *pgxpool.Pool, projectID uuid.UUID) (map[uuid.UUID]map[string]Run, error) {
	rows, err := pool.Query(ctx, `
SELECT DISTINCT ON (migration_id, env)
  id, run_type, migration_id, project_id, env, db_set_id, status, requested_by, requested_at, approved_by, approved_at, approval_comment, executed_by, started_at, finished_at, checksum_up_at_request, checksum_down_at_request
FROM runs
WHERE project_id = $1
ORDER BY migration_id, env, requested_at DESC
`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[uuid.UUID]map[string]Run)
	for rows.Next() {
		var run Run
		if err := rows.Scan(&run.ID, &run.RunType, &run.MigrationID, &run.ProjectID, &run.Env, &run.DBSetID, &run.Status, &run.RequestedBy, &run.RequestedAt, &run.ApprovedBy, &run.ApprovedAt, &run.ApprovalComment, &run.ExecutedBy, &run.StartedAt, &run.FinishedAt, &run.ChecksumUpAtRequest, &run.ChecksumDownAtRequest); err != nil {
			return nil, err
		}
		if _, ok := out[run.MigrationID]; !ok {
			out[run.MigrationID] = make(map[string]Run)
		}
		out[run.MigrationID][run.Env] = run
	}
	return out, rows.Err()
}

func ListLatestRunItemsByTargetMigration(ctx context.Context, pool *pgxpool.Pool, projectID uuid.UUID, env string) (map[uuid.UUID]map[uuid.UUID]LatestRunItem, error) {
	query := `
SELECT DISTINCT ON (ri.db_target_id, r.migration_id)
  ri.db_target_id, r.migration_id, r.id, r.run_type, r.status, ri.status, r.requested_at, r.finished_at
FROM run_items ri
JOIN runs r ON ri.run_id = r.id
WHERE r.project_id = $1
`
	args := []any{projectID}
	if env != "" {
		query += " AND r.env = $2"
		args = append(args, env)
	}
	query += " ORDER BY ri.db_target_id, r.migration_id, r.requested_at DESC"

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[uuid.UUID]map[uuid.UUID]LatestRunItem)
	for rows.Next() {
		var item LatestRunItem
		if err := rows.Scan(&item.TargetID, &item.MigrationID, &item.RunID, &item.RunType, &item.RunStatus, &item.ItemStatus, &item.RequestedAt, &item.FinishedAt); err != nil {
			return nil, err
		}
		if _, ok := out[item.TargetID]; !ok {
			out[item.TargetID] = make(map[uuid.UUID]LatestRunItem)
		}
		out[item.TargetID][item.MigrationID] = item
	}
	return out, rows.Err()
}

func itoa(v int) string {
	return strconv.Itoa(v)
}
