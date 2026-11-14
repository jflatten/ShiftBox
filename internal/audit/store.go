// Package audit appends bounded security evidence alongside protected intent.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Metadata deliberately contains identifiers only. Adding audit context requires
// an explicit schema change; arbitrary payloads and secrets are not accepted.
type Metadata struct {
	OwnerID string `json:"ownerId,omitempty"`
	GrantID string `json:"grantId,omitempty"`
}

// Event is a security decision without credential material or request bodies.
type Event struct {
	OccurredAt   time.Time
	ActorType    string
	ActorID      string
	Action       string
	ResourceType string
	ResourceID   string
	Outcome      string
	ReasonCode   string
	Metadata     Metadata
}

// Append inserts one event in the caller's intent transaction. There is no
// update operation: a later outcome is another event. Retention is separate from
// current authority and must never delete membership or resource records.
func Append(ctx context.Context, tx *sql.Tx, event Event) (int64, error) {
	if event.OccurredAt.IsZero() || event.ActorID == "" || event.Action == "" || event.ResourceType == "" || event.ResourceID == "" || event.Outcome == "" || event.ReasonCode == "" {
		return 0, errors.New("invalid audit event")
	}
	switch event.ActorType {
	case "user", "guest", "controller", "local_recovery":
	default:
		return 0, errors.New("invalid audit actor")
	}
	metadata, err := json.Marshal(event.Metadata)
	if err != nil || len(metadata) > 2048 {
		return 0, errors.New("invalid audit metadata")
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO audit_events(occurred_at,actor_type,actor_id,action,resource_type,resource_id,outcome,reason_code,metadata_json) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.ActorType, event.ActorID, event.Action, event.ResourceType, event.ResourceID, event.Outcome, event.ReasonCode, string(metadata)).Scan(&id)
	return id, err
}
