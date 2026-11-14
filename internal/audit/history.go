package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"
)

const historyPageSize = 50

// HistoryEvent is the bounded, public projection of an audit record.
type HistoryEvent struct {
	ID           string    `json:"id"`
	OccurredAt   time.Time `json:"occurredAt"`
	ActorType    string    `json:"actorType"`
	ActorID      string    `json:"actorId"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId"`
	Outcome      string    `json:"outcome"`
	ReasonCode   string    `json:"reasonCode"`
	Metadata     Metadata  `json:"metadata"`
}

// List returns the next page in reverse append order. Before is the exclusive
// sequence boundary from the previous page, or zero for the latest entries.
func List(ctx context.Context, tx *sql.Tx, installation, action, resource string, before int64) ([]HistoryEvent, int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,occurred_at,actor_type,actor_id,action,resource_type,resource_id,outcome,reason_code,metadata_json
		FROM audit_events WHERE ($1=0 OR id<$1) AND ($2='' OR action=$2) AND ($3='' OR resource_id=$3)
		ORDER BY id DESC LIMIT $4`, before, action, resource, historyPageSize+1)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]HistoryEvent, 0, historyPageSize)
	var lastID, next int64
	for rows.Next() {
		if len(items) == historyPageSize {
			next = lastID
			break
		}
		var item HistoryEvent
		var stamp, metadata string
		if err := rows.Scan(&lastID, &stamp, &item.ActorType, &item.ActorID, &item.Action, &item.ResourceType,
			&item.ResourceID, &item.Outcome, &item.ReasonCode, &metadata); err != nil {
			return nil, 0, err
		}
		item.OccurredAt, err = time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal([]byte(metadata), &item.Metadata); err != nil {
			return nil, 0, err
		}
		// Public IDs remain stable without changing the existing journal sequence.
		item.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(installation+"/audit/"+strconv.FormatInt(lastID, 10))).String()
		items = append(items, item)
	}
	return items, next, rows.Err()
}
