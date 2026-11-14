package operation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
)

// Journal serializes durable workflow changes through the shared database.
type Journal struct {
	db    *sql.DB
	now   func() time.Time
	newID func() string
}

// NewJournal receives clocks and identity generation explicitly for restart tests.
func NewJournal(db *sql.DB, now func() time.Time, newID func() string) *Journal {
	return &Journal{db: db, now: now, newID: newID}
}

// Create returns existing actor-scoped intent when an HTTP request is retried.
func (j *Journal) Create(ctx context.Context, request Request) (Operation, error) {
	var op Operation
	err := database.Transaction(ctx, j.db, func(tx *sql.Tx) error {
		var err error
		op, err = j.CreateInTx(ctx, tx, request)
		return err
	})
	return op, err
}

// CreateInTx lets admission commit authorization, reservations, and intent together.
// The caller owns commit/rollback and must never issue external effects in this transaction.
func (j *Journal) CreateInTx(ctx context.Context, tx *sql.Tx, request Request) (Operation, error) {
	if request.Actor == "" || len(request.Key) < 16 || len(request.Key) > 128 || request.Generation < 1 || len(request.Steps) == 0 {
		return Operation{}, errors.New("invalid operation intent")
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM operations WHERE requested_by=$1 AND request_key=$2`, request.Actor, request.Key).Scan(&id)
	if err == nil {
		return readOperation(ctx, tx, id)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	now := j.now().UTC()
	op := Operation{OriginatingAction: request.OriginatingAction, ID: j.newID(), WorkspaceID: request.WorkspaceID, Type: request.Type, Generation: request.Generation, Actor: request.Actor, RequestKey: request.Key, Status: "pending", CreatedAt: now, Version: 1, Steps: append([]Step(nil), request.Steps...)}
	if _, err := tx.ExecContext(ctx, `INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,created_at,version,originating_action) VALUES($1,$2,$3,$4,$5,$6,$7,$8,1,$9)`, op.ID, op.WorkspaceID, op.Type, op.Generation, op.Actor, op.RequestKey, op.Status, timestamp(now), op.OriginatingAction); err != nil {
		return Operation{}, err
	}
	for index := range op.Steps {
		step := &op.Steps[index]
		step.Index = index
		step.Status = "pending"
		step.Detail = ""
		if _, err := tx.ExecContext(ctx, `INSERT INTO operation_steps(operation_id,step_index,step_key,label,status) VALUES($1,$2,$3,$4,'pending')`, op.ID, index, step.Key, step.Label); err != nil {
			return Operation{}, err
		}
	}
	if err := appendEvent(ctx, tx, op, "", now); err != nil {
		return Operation{}, err
	}
	return op, nil
}

// Get reads a consistent operation and step snapshot.
func (j *Journal) Get(ctx context.Context, id string) (Operation, error) {
	var op Operation
	err := database.Transaction(ctx, j.db, func(tx *sql.Tx) error {
		var err error
		op, err = readOperation(ctx, tx, id)
		return err
	})
	return op, err
}

// GetInTx reads workflow evidence inside a caller-owned admission transaction.
func (j *Journal) GetInTx(ctx context.Context, tx *sql.Tx, id string) (Operation, error) {
	return readOperation(ctx, tx, id)
}

// Claim leases restartable work and gives each worker attempt a distinct fence.
func (j *Journal) Claim(ctx context.Context, id, worker string, duration time.Duration) (Claim, error) {
	var claim Claim
	if worker == "" || duration <= 0 {
		return claim, ErrConflict
	}
	err := database.Transaction(ctx, j.db, func(tx *sql.Tx) error {
		op, err := readOperation(ctx, tx, id)
		if err != nil {
			return err
		}
		now := j.now().UTC()
		if op.Status == "succeeded" || op.Status == "cancelled" || op.Status == "failed" || op.leaseExpiresAt.After(now) {
			return ErrConflict
		}
		op.leaseOwner = worker + "/" + j.newID()
		op.leaseExpiresAt = now.Add(duration)
		op.Status = "running"
		if op.StartedAt == nil {
			op.StartedAt = &now
		}
		claim = Claim{OperationID: id, Generation: op.Generation, Token: op.leaseOwner}
		return saveOperation(ctx, tx, &op, "", now)
	})
	return claim, err
}

func (j *Journal) change(ctx context.Context, claim Claim, evidence string, change func(*Operation) error) error {
	return database.Transaction(ctx, j.db, func(tx *sql.Tx) error {
		op, err := readOperation(ctx, tx, claim.OperationID)
		if err != nil {
			return err
		}
		now := j.now().UTC()
		if op.Generation != claim.Generation || op.leaseOwner != claim.Token || !op.leaseExpiresAt.After(now) || op.Status != "running" {
			return ErrConflict
		}
		if err := change(&op); err != nil {
			return err
		}
		return saveOperation(ctx, tx, &op, evidence, now)
	})
}

// Renew extends only a currently valid claim; expired workers cannot regain authority.
func (j *Journal) Renew(ctx context.Context, claim Claim, duration time.Duration) error {
	if duration <= 0 {
		return ErrConflict
	}
	return j.change(ctx, claim, "", func(op *Operation) error { op.leaseExpiresAt = j.now().Add(duration); return nil })
}

// StartStep durably records the active step before any external effect.
func (j *Journal) StartStep(ctx context.Context, claim Claim, index int) error {
	return j.change(ctx, claim, "", func(op *Operation) error {
		if index < 0 || index >= len(op.Steps) {
			return ErrConflict
		}
		for _, step := range op.Steps[:index] {
			if step.Status != "succeeded" && step.Status != "skipped" {
				return ErrConflict
			}
		}
		if op.Steps[index].Status != "pending" && op.Steps[index].Status != "active" {
			return ErrConflict
		}
		op.Steps[index].Status = "active"
		key := op.Steps[index].Key
		op.CurrentStep = &key
		return nil
	})
}

// CompleteStep commits verified evidence and step completion atomically.
func (j *Journal) CompleteStep(ctx context.Context, claim Claim, index int, evidence string) error {
	if evidence == "" || len(evidence) > 4096 {
		return errors.New("completion requires bounded non-secret evidence")
	}
	return j.change(ctx, claim, evidence, func(op *Operation) error {
		if index < 0 || index >= len(op.Steps) || op.Steps[index].Status != "active" {
			return ErrConflict
		}
		op.Steps[index].Status = "succeeded"
		if index == len(op.Steps)-1 {
			now := j.now().UTC()
			op.Status = "succeeded"
			op.FinishedAt = &now
			op.CurrentStep = nil
		}
		return nil
	})
}

// Events replays a bounded ordered suffix after the last acknowledged event ID.
func (j *Journal) Events(ctx context.Context, id string, after int64) ([]Event, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT payload_json FROM operation_events WHERE operation_id=$1 AND event_id>$2 ORDER BY event_id LIMIT 1000`, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var event Event
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// LatestEventID provides the durable replay boundary without assuming event IDs equal versions.
func (j *Journal) LatestEventID(ctx context.Context, id string) (int64, error) {
	var latest int64
	err := j.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(event_id),0) FROM operation_events WHERE operation_id=$1`, id).Scan(&latest)
	return latest, err
}

// RecordLocalStepInTx records a one-step setup operation whose effect is entirely
// in the caller's transaction. A retry returns the original committed evidence.
func (j *Journal) RecordLocalStepInTx(ctx context.Context, tx *sql.Tx, request Request, evidence string) (Operation, error) {
	if request.Type != "setup" || len(request.Steps) != 1 || evidence == "" || len(evidence) > 4096 {
		return Operation{}, ErrConflict
	}
	op, err := j.CreateInTx(ctx, tx, request)
	if err != nil {
		return Operation{}, err
	}
	if op.Type != request.Type || op.OriginatingAction != request.OriginatingAction ||
		len(op.Steps) != 1 || op.Steps[0].Key != request.Steps[0].Key {
		return Operation{}, ErrConflict
	}
	if op.Status == "succeeded" && op.Steps[0].Status == "succeeded" && op.Steps[0].Detail == evidence {
		return op, nil
	}
	if op.Status != "pending" || op.Steps[0].Status != "pending" {
		return Operation{}, ErrConflict
	}
	now := j.now().UTC()
	op.Status = "succeeded"
	op.StartedAt, op.FinishedAt = &now, &now
	op.Steps[0].Status, op.Steps[0].Detail = "succeeded", evidence
	if err := saveOperation(ctx, tx, &op, evidence, now); err != nil {
		return Operation{}, err
	}
	return op, nil
}
