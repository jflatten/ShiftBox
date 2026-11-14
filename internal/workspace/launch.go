package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

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
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT id FROM operations WHERE requested_by=$1 AND request_key=$2`, launch.OwnerID, launch.RequestKey).Scan(&existing)
	if err == nil {
		return replayLaunch(ctx, tx, journal, existing, launch)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return operation.Operation{}, err
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
	result, err = tx.ExecContext(ctx, `INSERT INTO workspaces
		(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
		repository_url,repository_ref,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,
		secret_names_json,retention_seconds,launch_retention_seconds,desired_state,observed_state,generation,ownership_marker,created_at,updated_at,version)
		SELECT $1,$2,$3,$4,$5,id,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15,'running','unknown',1,$16,$17,$17,1
		FROM platform_artifacts WHERE id=$6 AND state='available'`,
		launch.WorkspaceID, launch.InstallationID, launch.OwnerID, launch.TemplateID, launch.TemplateRevision, launch.ArtifactID,
		launch.RepositoryURL, launch.RepositoryRef, launch.DevcontainerPath, launch.CPU, launch.MemoryMiB, launch.DiskMiB,
		launch.NetworkPolicyJSON, string(secretNames), launch.RetentionSeconds, launch.OwnershipMarker, stamp)
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
		},
	})
	if err != nil {
		return operation.Operation{}, err
	}
	if op.WorkspaceID == nil || *op.WorkspaceID != launch.WorkspaceID ||
		op.Type != "provision" || op.OriginatingAction != "workspace.launch" {
		return operation.Operation{}, ErrLaunchConflict
	}
	return op, nil
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
		AND COALESCE(launch_retention_seconds,0)=COALESCE(CAST($6 AS BIGINT),0))`,
		*op.WorkspaceID, launch.InstallationID, launch.OwnerID, launch.TemplateID, launch.TemplateRevision, launch.RetentionSeconds).Scan(&matching)
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
		(launch.RetentionSeconds == nil || *launch.RetentionSeconds > 0) &&
		template.ValidSource(launch.RepositoryURL, launch.DevcontainerPath) &&
		json.Unmarshal([]byte(launch.NetworkPolicyJSON), &network) == nil && len(network) != 0
}
