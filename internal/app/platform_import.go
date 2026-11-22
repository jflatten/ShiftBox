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

// Unlike a local path or a Packer JSON object, this durable source begins with the official selector.
const officialManifestPrefix = "official-ami:"

// PlatformImport binds management admission to the durable catalog worker.
type PlatformImport struct {
	db            *sql.DB
	journal       *operation.Journal
	origin        string
	installation  string
	now           func() time.Time
	providerKind  string
	officialImage *release.OfficialEC2Image
	run           func(ctx context.Context, operationID, actor, input string, options PlatformImportOptions) error
}

// PlatformImportOptions records the administrator's publication choices.
type PlatformImportOptions struct {
	AMIFamily   string
	MakeDefault bool
	Official    bool
}

// NewPlatformImport builds the active import handler and its worker.
func NewPlatformImport(db *sql.DB, journal *operation.Journal, origin, installation string, now func() time.Time,
	run func(context.Context, string, string, string, PlatformImportOptions) error, providerKind string,
	officialImage *release.OfficialEC2Image) *PlatformImport {
	if officialImage != nil {
		pin := *officialImage
		officialImage = &pin
	}
	return &PlatformImport{db: db, journal: journal, origin: origin, installation: installation,
		now: now, run: run, providerKind: providerKind, officialImage: officialImage}
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
		Folder           string          `json:"folder"`
		Manifest         json.RawMessage `json:"manifest"`
		OfficialManifest *string         `json:"officialManifest"`
		MakeDefault      bool            `json:"makeDefault"`
		AMIFamily        string          `json:"amiFamily"`
	}
	key, err := web.IdempotencyKey(r)
	if err != nil {
		return nil, err
	}
	if err := web.DecodeJSON(nil, r, &input, 8192); err != nil {
		return nil, err
	}
	source := input.Folder
	switch {
	case p.providerKind == "ec2" && input.OfficialManifest != nil:
		catalog, err := release.ReadEC2Manifest(strings.NewReader(*input.OfficialManifest))
		if input.Folder != "" || len(input.Manifest) != 0 || input.AMIFamily != "" || err != nil ||
			p.officialImage == nil || !p.officialImage.MatchesManifest(catalog) {
			return nil, web.Problem{Status: 400, Code: "infrastructure.invalid_official_manifest",
				Title:  "The official AMI catalog does not match this installation",
				Detail: "Upload the unmodified regional catalog JSON from this ShiftBox release's support archive."}
		}
		source = officialManifestPrefix + *input.OfficialManifest
	case p.providerKind == "ec2":
		source = string(input.Manifest)
		if _, err := release.ReadEC2Manifest(strings.NewReader(source)); input.Folder != "" || err != nil {
			return nil, web.Problem{Status: 400, Code: "infrastructure.invalid_import_manifest", Title: "Provide the Packer AMI manifest"}
		}
		if input.AMIFamily != "" && (!release.ValidEC2FamilyName(input.AMIFamily) || input.MakeDefault) {
			return nil, web.Problem{Status: 400, Code: "infrastructure.invalid_ami_family", Title: "Choose a valid AMI family without changing the default image"}
		}
	case len(input.Manifest) != 0 || input.OfficialManifest != nil || input.AMIFamily != "" ||
		!filepath.IsAbs(source) || len(source) > 4096 || strings.ContainsRune(source, 0):
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO platform_imports(operation_id,installation_id,folder,make_default,ec2_family)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT(operation_id) DO NOTHING`, op.ID, p.installation, source, defaultValue(input.MakeDefault), input.AMIFamily); err != nil {
		return nil, err
	}
	var matching bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM platform_imports
		WHERE operation_id=$1 AND installation_id=$2 AND folder=$3 AND make_default=$4 AND ec2_family=$5)`,
		op.ID, p.installation, source, defaultValue(input.MakeDefault), input.AMIFamily).Scan(&matching); err != nil {
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
func (p *PlatformImport) Run(ctx context.Context, enter func(context.Context) (func(), error)) error {
	worker := p.worker()
	worker.EnterActivity = enter
	return worker.Run(ctx, time.Second)
}

func (p *PlatformImport) worker() *operation.Worker {
	return &operation.Worker{Journal: p.journal, Name: "platform-import", LeaseDuration: 18 * time.Minute,
		Executor: platformImportExecutor{p}, Actions: []string{platformImportAction}}
}

type platformImportExecutor struct{ *PlatformImport }

// Observe defers reconciliation to the catalog import, which can verify a published copy.
func (e platformImportExecutor) Observe(context.Context, operation.Operation, operation.Step) (string, bool, error) {
	// Catalog validation checks immutable publication before replaying its boot check.
	return "", false, nil
}

// Apply rechecks administrator authority before importing the recorded image.
func (e platformImportExecutor) Apply(ctx context.Context, op operation.Operation, step operation.Step) (string, error) {
	if op.OriginatingAction != platformImportAction || step.Key != "import" {
		return "", operation.ErrConflict
	}
	var folder string
	var makeDefault int
	var options PlatformImportOptions
	err := e.db.QueryRowContext(ctx, `SELECT p.folder,p.make_default,p.ec2_family FROM platform_imports p
		JOIN installations i ON i.id=p.installation_id JOIN users u ON u.id=$3
		WHERE p.operation_id=$1 AND i.id=$2 AND i.state='active'
		  AND u.role='administrator' AND u.membership_state='active'`, op.ID, e.installation, op.Actor).Scan(&folder, &makeDefault, &options.AMIFamily)
	if errors.Is(err, sql.ErrNoRows) {
		return "", &operation.StepFailure{Detail: "Platform import authorization is no longer active."}
	}
	if err != nil {
		return "", err
	}
	options.MakeDefault = makeDefault != 0
	if e.providerKind == "ec2" {
		folder, options.Official = strings.CutPrefix(folder, officialManifestPrefix)
	}
	if err := e.run(ctx, op.ID, op.Actor, folder, options); err != nil {
		if e.providerKind == "ec2" {
			if errors.Is(err, release.ErrInvalidArtifact) || errors.Is(err, release.ErrCatalogConflict) ||
				errors.Is(err, provider.ErrCapabilityUnsupported) || errors.Is(err, errCanaryRetryRequiresNewOperation) {
				return "", &operation.StepFailure{Detail: "Check the AMI provenance, verified enrollment, region and guest release before starting a new import."}
			}
			return "", &operation.StepWaiting{Detail: "Waiting for EC2 to verify the disposable image boot and cleanup.", Cause: err}
		}
		if errors.Is(err, release.ErrInvalidArtifact) || errors.Is(err, release.ErrCatalogConflict) || errors.Is(err, provider.ErrCapabilityUnsupported) {
			return "", &operation.StepFailure{Detail: "Check the local bundle and manifest, the supported UTM version, and the guest release."}
		}
		return "", &operation.StepWaiting{Detail: "Waiting for UTM to verify the disposable platform image boot and cleanup.", Cause: err}
	}
	if e.providerKind == "ec2" {
		return "The verified EC2 image is available in the regional catalog.", nil
	}
	return "The verified platform image is available in the local catalog.", nil
}
