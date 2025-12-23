package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Logger interface {
	Error(msg string, args ...any)
}

type Event struct {
	ActorID    *uuid.UUID
	Action     string
	EntityType string
	EntityID   *uuid.UUID
	Payload    map[string]any
}

func LogEvent(ctx context.Context, pool *pgxpool.Pool, logger Logger, event Event) error {
	payload := event.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal audit payload: %w", err)
	}

	id := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO audit_events (id, actor_id, action, entity_type, entity_id, payload)
VALUES ($1, $2, $3, $4, $5, $6)
`, id, event.ActorID, event.Action, event.EntityType, event.EntityID, body); err != nil {
		if logger != nil {
			logger.Error("audit log failed", "error", err)
		}
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}
