package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/web"
)

const platformImportAction = "infrastructure.import_platform"

// PlatformImport binds management admission to the durable catalog worker.
type PlatformImport struct {
	db           *sql.DB
	journal      *operation.Journal
	origin       string
	installation string
	now          func() time.Time
	run          func(ctx context.Context, operationID, actor, folder string, makeDefault bool) error
}

// NewPlatformImport builds the active import handler and its worker.
func NewPlatformImport(db *sql.DB, journal *operation.Journal, origin, installation string, now func() time.Time,
	run func(context.Context, string, string, string, bool) error) *PlatformImport {
	return &PlatformImport{db: db, journal: journal, origin: origin, installation: installation, now: now, run: run}
}

// Handler authorizes administrators and commits the import before booting anything.
func (p *PlatformImport) Handler() web.AuthorizedHandler {
	return web.AuthorizedHandler{
		DB: p.db, Origin: p.origin, Now: p.now,
		Route: web.ManagementRoute{Method: http.MethodPost, Path: "/admin/platform-artifacts", OperationID: "importPlatformArtifact", Action: "infrastructure.manage"},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (web.AuthorizationTarget, error) {
			return web.AuthorizationTarget{Action: "infrastructure.manage", Resource: auth.Resource{Type: "installation", ID: p.installation}}, nil
		},
		Apply: p.admit,
	}
}

func (p *PlatformImport) admit(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, _ web.AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
	var input struct {
		Folder      string `json:"folder"`
		MakeDefault bool   `json:"makeDefault"`
	}
	key, err := web.IdempotencyKey(r)
	if err != nil {
		return nil, err
	}
	if err := web.DecodeJSON(nil, r, &input, 8192); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(input.Folder) || len(input.Folder) > 4096 || strings.ContainsRune(input.Folder, 0) {
		return nil, web.Problem{Status: 400, Code: "infrastructure.invalid_import_folder", Title: "Enter an absolute folder on the controller"}
	}
	// Serialize actor-scoped keys with other installation admissions.
	result, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE id=$1 AND state='active'", p.installation)
	if err != nil {
		return nil, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return nil, errors.Join(err, operation.ErrConflict)
	}
	op, err := p.journal.CreateInTx(ctx, tx, operation.Request{
		Actor: actor.User.ID, Key: key, Type: "setup", Generation: 1, OriginatingAction: platformImportAction,
		Steps: []operation.Step{{Key: "import", Label: "Import and verify the platform image"}},
	})
	if err != nil {
		return nil, err
	}
	conflict := web.Problem{Status: 409, Code: "infrastructure.import_conflict", Title: "This request identifier is already in use"}
	if op.OriginatingAction != platformImportAction || op.Type != "setup" || op.WorkspaceID != nil {
		return nil, conflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO platform_imports(operation_id,installation_id,folder,make_default)
		VALUES($1,$2,$3,$4) ON CONFLICT(operation_id) DO NOTHING`, op.ID, p.installation, input.Folder, defaultValue(input.MakeDefault)); err != nil {
		return nil, err
	}
	var matching bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM platform_imports
		WHERE operation_id=$1 AND installation_id=$2 AND folder=$3 AND make_default=$4)`,
		op.ID, p.installation, input.Folder, defaultValue(input.MakeDefault)).Scan(&matching); err != nil {
		return nil, err
	}
	if !matching {
		return nil, conflict
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(op)
	}), nil
}

func defaultValue(enabled bool) int {
	if enabled {
		return 1
	}
	return 0
}

// Run reconciles catalog imports independently of workspace power operations.
func (p *PlatformImport) Run(ctx context.Context) error {
	return p.worker().Run(ctx, time.Second)
}

func (p *PlatformImport) worker() *operation.Worker {
	return &operation.Worker{Journal: p.journal, Name: "platform-import", LeaseDuration: 18 * time.Minute,
		Executor: platformImportExecutor{p}, Actions: []string{platformImportAction}}
}

type platformImportExecutor struct{ *PlatformImport }

func (e platformImportExecutor) Observe(context.Context, operation.Operation, operation.Step) (string, bool, error) {
	// Catalog validation checks immutable publication before replaying its boot check.
	return "", false, nil
}

func (e platformImportExecutor) Apply(ctx context.Context, op operation.Operation, step operation.Step) (string, error) {
	if op.OriginatingAction != platformImportAction || step.Key != "import" {
		return "", operation.ErrConflict
	}
	var folder string
	var makeDefault int
	err := e.db.QueryRowContext(ctx, `SELECT p.folder,p.make_default FROM platform_imports p
		JOIN installations i ON i.id=p.installation_id JOIN users u ON u.id=$3
		WHERE p.operation_id=$1 AND i.id=$2 AND i.state='active'
		  AND u.role='administrator' AND u.membership_state='active'`, op.ID, e.installation, op.Actor).Scan(&folder, &makeDefault)
	if errors.Is(err, sql.ErrNoRows) {
		return "", &operation.StepFailure{Detail: "Platform import authorization is no longer active."}
	}
	if err != nil {
		return "", err
	}
	if err := e.run(ctx, op.ID, op.Actor, folder, makeDefault != 0); err != nil {
		if errors.Is(err, release.ErrInvalidArtifact) || errors.Is(err, release.ErrCatalogConflict) || errors.Is(err, provider.ErrCapabilityUnsupported) {
			return "", &operation.StepFailure{Detail: "Check the local bundle and manifest, the supported UTM version, and the guest release."}
		}
		return "", &operation.StepWaiting{Detail: "Waiting for UTM to verify the disposable platform image boot and cleanup.", Cause: err}
	}
	return "The verified platform image is available in the local catalog.", nil
}
