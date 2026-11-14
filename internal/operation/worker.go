package operation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (j *Journal) due(ctx context.Context, actions []string) ([]string, error) {
	query := `SELECT id FROM operations WHERE status IN ('pending','running','waiting')
		AND (lease_expires_at IS NULL OR lease_expires_at <= $1)`
	args := []any{timestamp(j.now())}
	if len(actions) > 0 {
		params := make([]string, 0, len(actions))
		for _, action := range actions {
			args = append(args, action)
			params = append(params, fmt.Sprintf("$%d", len(args)))
		}
		query += " AND originating_action IN (" + strings.Join(params, ",") + ")"
	}
	rows, err := j.db.QueryContext(ctx, query+" ORDER BY created_at,id LIMIT 100", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Run schedules due work until cancellation. Uncertain effects remain visible
// and retryable; database failures return to the controller supervisor.
func (w *Worker) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("worker interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ids, err := w.Journal.due(ctx, w.Actions)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := w.runActivity(ctx, id); err != nil && !errors.Is(err, ErrConflict) {
				op, readErr := w.Journal.Get(ctx, id)
				if readErr != nil {
					return readErr
				}
				if op.Status != "waiting" {
					return err
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Worker) runActivity(ctx context.Context, id string) error {
	if w.EnterActivity == nil {
		return w.RunOnce(ctx, id)
	}
	leave, err := w.EnterActivity(ctx)
	if err != nil {
		return err
	}
	defer leave()
	return w.RunOnce(ctx, id)
}
