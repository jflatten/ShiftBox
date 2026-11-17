package workspace

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/template"
)

// ErrLaunchConflict indicates a reused request key with different launch input.
var ErrLaunchConflict = errors.New("workspace launch intent conflict")

// LaunchIntent captures a launch after the caller resolves and authorizes the
// template revision, retention policy, profile, artifact and effective network
// in the admission transaction. Only template, revision, retention and the
// request key originate in the launch request.
type LaunchIntent struct {
	InstallationID, WorkspaceID, OwnerID, RequestKey string
	TemplateID, ArtifactID                           string
	ProviderKind                                     string
	SingleBoot                                       bool
	Name                                             string
	TemplateRevision                                 int64
	RepositoryURL, DevcontainerPath                  string
	RepositoryRef                                    *string
	Allocation
	NetworkPolicyJSON string
	SecretNames       []string
	OwnershipMarker   string
	RetentionSeconds  *int64
}

// AdmitLaunch persists the snapshot, capacity reservation and operation in the
// caller's transaction. Any error requires rolling back the transaction.
func AdmitLaunch(ctx context.Context, tx *sql.Tx, journal *operation.Journal, launch LaunchIntent, now time.Time) (operation.Operation, error) {
	if !launch.valid(now) {
		return operation.Operation{}, provider.ErrInvalidState
	}
	// One installation owns all launch requests. Lock it before looking up
	// the key so competing requests reconcile without a duplicate snapshot.
	result, err := tx.ExecContext(ctx, `UPDATE installations SET version=version WHERE id=$1 AND state='active'`, launch.InstallationID)
	if err := capacityWrite(result, err); err != nil {
		return operation.Operation{}, err
	}
	if op, found, err := FindLaunch(ctx, tx, journal, launch); found || err != nil {
		return op, err
	}
	kind := launch.ProviderKind
	if kind == "" {
		kind = "utm"
	}
	if kind == "ec2" {
		launch.InstanceType = launch.EC2Type()
		if launch.InstanceType == "" || launch.DiskMiB%1024 != 0 {
			return operation.Operation{}, provider.ErrCapabilityUnsupported
		}
	} else if launch.InstanceType != "" {
		return operation.Operation{}, provider.ErrCapabilityUnsupported
	}

	names := launch.SecretNames
	if names == nil {
		names = []string{}
	}
	secretNames, err := json.Marshal(names)
	if err != nil {
		return operation.Operation{}, err
	}
	const generation = 1
	stamp := now.UTC().Format(time.RFC3339Nano)
	deadline := RetentionDeadline(now, launch.RetentionSeconds)
	result, err = tx.ExecContext(ctx, `INSERT INTO workspaces
		(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
		repository_url,repository_ref,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,
		secret_names_json,retention_seconds,launch_retention_seconds,desired_state,observed_state,generation,ownership_marker,created_at,updated_at,version,retention_deadline,name,instance_type)
		SELECT $1,$2,$3,$4,$5,id,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15,'running','unknown',1,$16,$17,$17,1,$18,$19,$21
		FROM platform_artifacts WHERE id=$6 AND state='available' AND provider_kind=$20`,
		launch.WorkspaceID, launch.InstallationID, launch.OwnerID, launch.TemplateID, launch.TemplateRevision, launch.ArtifactID,
		launch.RepositoryURL, launch.RepositoryRef, launch.DevcontainerPath, launch.CPU, launch.MemoryMiB, launch.DiskMiB,
		launch.NetworkPolicyJSON, string(secretNames), launch.RetentionSeconds, launch.OwnershipMarker, stamp, deadline, launch.Name, kind, launch.InstanceType)
	if err := capacityWrite(result, err); err != nil {
		return operation.Operation{}, err
	}
	if err := ReserveStart(ctx, tx, launch.InstallationID, launch.WorkspaceID, generation, now); err != nil {
		return operation.Operation{}, err
	}
	op, err := journal.CreateInTx(ctx, tx, operation.Request{
		Actor: launch.OwnerID, Key: launch.RequestKey, Type: "provision", OriginatingAction: "workspace.launch",
		WorkspaceID: &launch.WorkspaceID, Generation: generation,
		Steps: []operation.Step{
			{Key: "provision", Label: "Provision workspace"},
			{Key: "enroll", Label: "Enroll the guest"},
			{Key: "configure", Label: "Configure the development container"},
			{Key: "verify_ide", Label: "Verify the browser IDE"},
			{Key: "finish_setup", Label: "Finish remaining setup hooks"},
		},
	})
	if err != nil {
		return operation.Operation{}, err
	}
	if op.WorkspaceID == nil || *op.WorkspaceID != launch.WorkspaceID ||
		op.Type != "provision" || op.OriginatingAction != "workspace.launch" {
		return operation.Operation{}, ErrLaunchConflict
	}
	if launch.SingleBoot {
		if kind != "ec2" {
			return operation.Operation{}, provider.ErrInvalidState
		}
		var reference [32]byte
		if _, err := rand.Read(reference[:]); err != nil {
			return operation.Operation{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ec2_launch_enrollment(operation_id,launch_reference) VALUES($1,$2)`,
			op.ID, hex.EncodeToString(reference[:])); err != nil {
			return operation.Operation{}, err
		}
	}
	return op, nil
}

// FindLaunch checks for a committed request before reevaluating provider and
// profile eligibility. Call only after checking current launch authorization.
func FindLaunch(ctx context.Context, tx *sql.Tx, journal *operation.Journal, launch LaunchIntent) (operation.Operation, bool, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM operations WHERE requested_by=$1 AND request_key=$2`, launch.OwnerID, launch.RequestKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return operation.Operation{}, false, nil
	}
	if err != nil {
		return operation.Operation{}, false, err
	}
	op, err := replayLaunch(ctx, tx, journal, id, launch)
	return op, true, err
}

func replayLaunch(ctx context.Context, tx *sql.Tx, journal *operation.Journal, id string, launch LaunchIntent) (operation.Operation, error) {
	op, err := journal.GetInTx(ctx, tx, id)
	if err != nil {
		return operation.Operation{}, err
	}
	if op.Type != "provision" || op.OriginatingAction != "workspace.launch" || op.WorkspaceID == nil {
		return operation.Operation{}, ErrLaunchConflict
	}
	var matching bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspaces
		WHERE id=$1 AND installation_id=$2 AND owner_user_id=$3 AND template_id=$4 AND template_revision=$5
		AND COALESCE(launch_retention_seconds,0)=COALESCE(CAST($6 AS BIGINT),0) AND name=$7)`,
		*op.WorkspaceID, launch.InstallationID, launch.OwnerID, launch.TemplateID, launch.TemplateRevision, launch.RetentionSeconds, launch.Name).Scan(&matching)
	if err != nil {
		return operation.Operation{}, err
	}
	if !matching {
		return operation.Operation{}, ErrLaunchConflict
	}
	return op, nil
}

func (launch LaunchIntent) valid(now time.Time) bool {
	var network map[string]json.RawMessage
	return !now.IsZero() && launch.InstallationID != "" && launch.WorkspaceID != "" && launch.OwnerID != "" &&
		launch.TemplateID != "" && launch.TemplateRevision > 0 && launch.ArtifactID != "" && launch.OwnershipMarker != "" &&
		len(launch.RequestKey) >= 16 && len(launch.RequestKey) <= 128 && launch.Allocation.positive() &&
		(launch.Name == "" || ValidName(launch.Name)) &&
		(launch.RetentionSeconds == nil || RetentionDeadline(now, launch.RetentionSeconds) != nil) &&
		(launch.RepositoryRef == nil || template.ValidRepositoryRef(*launch.RepositoryRef)) &&
		template.ValidSource(launch.RepositoryURL, launch.DevcontainerPath) &&
		json.Unmarshal([]byte(launch.NetworkPolicyJSON), &network) == nil && len(network) != 0
}

// ValidName reports whether a normalized workspace display name is usable.
func ValidName(name string) bool {
	return name != "" && name == strings.TrimSpace(name) && utf8.RuneCountInString(name) <= 120 &&
		!strings.ContainsFunc(name, unicode.IsControl)
}
