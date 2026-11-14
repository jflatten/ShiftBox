package operation

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Fixed-width UTC strings preserve chronological ordering in both SQL dialects.
func timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }

func optionalTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return timestamp(*t)
}

func readOperation(ctx context.Context, tx *sql.Tx, id string) (Operation, error) {
	var op Operation
	var created string
	var started, finished, leaseOwner, leaseExpires sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT originating_action,id,workspace_id,type,generation,requested_by,request_key,status,current_step,created_at,started_at,finished_at,version,lease_owner,lease_expires_at FROM operations WHERE id=$1`, id).Scan(&op.OriginatingAction, &op.ID, &op.WorkspaceID, &op.Type, &op.Generation, &op.Actor, &op.RequestKey, &op.Status, &op.CurrentStep, &created, &started, &finished, &op.Version, &leaseOwner, &leaseExpires)
	if err != nil {
		return Operation{}, err
	}
	op.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Operation{}, err
	}
	for _, field := range []struct {
		source sql.NullString
		target **time.Time
	}{{started, &op.StartedAt}, {finished, &op.FinishedAt}} {
		if field.source.Valid {
			value, err := time.Parse(time.RFC3339Nano, field.source.String)
			if err != nil {
				return Operation{}, err
			}
			*field.target = &value
		}
	}
	op.leaseOwner = leaseOwner.String
	if leaseExpires.Valid {
		op.leaseExpiresAt, err = time.Parse(time.RFC3339Nano, leaseExpires.String)
		if err != nil {
			return Operation{}, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT step_key,label,status,step_index,COALESCE(detail,'') FROM operation_steps WHERE operation_id=$1 ORDER BY step_index`, id)
	if err != nil {
		return Operation{}, err
	}
	defer rows.Close()
	op.Steps = []Step{}
	for rows.Next() {
		var step Step
		if err := rows.Scan(&step.Key, &step.Label, &step.Status, &step.Index, &step.Detail); err != nil {
			return Operation{}, err
		}
		op.Steps = append(op.Steps, step)
	}
	return op, rows.Err()
}

func appendEvent(ctx context.Context, tx *sql.Tx, op Operation, evidence string, now time.Time) error {
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(event_id),0)+1 FROM operation_events WHERE operation_id=$1`, op.ID).Scan(&id); err != nil {
		return err
	}
	event := Event{ID: id, Operation: op, Evidence: evidence, OccurredAt: now}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO operation_events(operation_id,event_id,operation_version,event_type,occurred_at,payload_json) VALUES($1,$2,$3,'operation.updated',$4,$5)`, op.ID, id, op.Version, timestamp(now), string(payload))
	return err
}

func saveOperation(ctx context.Context, tx *sql.Tx, op *Operation, evidence string, now time.Time) error {
	op.Version++
	var expires any
	if !op.leaseExpiresAt.IsZero() {
		expires = timestamp(op.leaseExpiresAt)
	}
	_, err := tx.ExecContext(ctx, `UPDATE operations SET status=$1,current_step=$2,started_at=$3,finished_at=$4,version=$5,lease_owner=$6,lease_expires_at=$7 WHERE id=$8`, op.Status, op.CurrentStep, optionalTime(op.StartedAt), optionalTime(op.FinishedAt), op.Version, op.leaseOwner, expires, op.ID)
	if err != nil {
		return err
	}
	for _, step := range op.Steps {
		if _, err := tx.ExecContext(ctx, `UPDATE operation_steps SET status=$1,detail=$2,
			started_at=CASE WHEN $1 IN ('active','succeeded','failed') THEN COALESCE(started_at,$5) ELSE started_at END,
			finished_at=CASE WHEN $1 IN ('succeeded','failed','skipped') THEN COALESCE(finished_at,$5) ELSE finished_at END
			WHERE operation_id=$3 AND step_index=$4`, step.Status, step.Detail, op.ID, step.Index, timestamp(now)); err != nil {
			return err
		}
	}
	return appendEvent(ctx, tx, *op, evidence, now)
}
