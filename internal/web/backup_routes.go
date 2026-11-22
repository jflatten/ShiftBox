package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/operation"
)

// BackupJob keeps the archive destination and passphrase in controller memory.
type BackupJob struct {
	id, destination, password string
}

// BackupRoute admits an administrator backup before handing its private
// inputs to the controller worker.
func BackupRoute(db *sql.DB, service *backup.Service, origin, installation string, now func() time.Time,
	enqueue func(BackupJob)) AuthorizedHandler {
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodPost, Path: "/backups", OperationID: "createBackup", Action: "backup.create"},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
			return AuthorizationTarget{Action: "backup.create", Resource: auth.Resource{Type: "installation", ID: installation}}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			key, err := IdempotencyKey(r)
			if err != nil {
				return nil, err
			}
			var input struct {
				Password    string `json:"password"`
				Destination string `json:"destination"`
			}
			if err := DecodeJSON(nil, r, &input, 8192); err != nil {
				return nil, err
			}
			if utf8.RuneCountInString(input.Password) < 12 || len(input.Password) > 1024 ||
				!filepath.IsAbs(input.Destination) || filepath.Clean(input.Destination) != input.Destination ||
				input.Destination == "/" || len(input.Destination) > 4096 || strings.ContainsRune(input.Destination, 0) {
				return nil, Problem{Status: 400, Code: "backup.invalid_input", Title: "Enter an absolute archive path and a passphrase of at least 12 characters"}
			}
			op, created, err := service.Queue(ctx, tx, actor.User.ID, key)
			if errors.Is(err, operation.ErrConflict) {
				return nil, Problem{Status: 409, Code: "backup.conflict", Title: "Finish the current backup or use a new request identifier"}
			}
			if err != nil {
				return nil, err
			}
			accepted := noticeJSON(http.StatusAccepted, op, "")
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if created {
					enqueue(BackupJob{id: op.ID, destination: input.Destination, password: input.Password})
				}
				accepted.ServeHTTP(w, r)
			}), nil
		},
	}
}

// RunBackupJobs keeps one admitted backup in progress at a time.
func RunBackupJobs(ctx context.Context, jobs <-chan BackupJob, service *backup.Service,
	capture func(context.Context, string) (map[string]string, error)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case job, ok := <-jobs:
			if !ok {
				return nil
			}
			// The service records a sanitized failure or verified completion.
			_, _ = service.Run(ctx, job.id, job.destination, job.password, capture)
		}
	}
}
