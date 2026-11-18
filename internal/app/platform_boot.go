package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/web"
)

// PlatformBootProvider imports the catalog's disposable copy and owns its lifecycle.
type PlatformBootProvider interface {
	provider.Provider
	ImportCandidate(context.Context, provider.CreateRequest, release.Artifact) (provider.Inventory, error)
}

var errCanaryRetryRequiresNewOperation = errors.New("previous canary cleaned up; retry with a new import operation")

// ValidatePlatformBoot checks a candidate with an isolated CA, database and
// enrollment listener. Its only success signal is an authenticated poll with
// the manifest's exact versions, followed by verified removal of the clone.
func ValidatePlatformBoot(ctx context.Context, root string, machines PlatformBootProvider, req provider.CreateRequest,
	artifact release.Artifact, config GuestListenerConfig) (result error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	if config.GitHub != nil || config.Secrets != nil || config.Tunnels != nil || req.Ref.InstanceID != "" || req.EnrollmentMedia != "" {
		return provider.ErrInvalidState
	}
	if artifact.EC2 != nil {
		// An earlier canary cannot authenticate against the new isolated CA.
		_, err := machines.Inspect(ctx, req.Ref)
		if err == nil {
			if err := removePlatformBootVM(ctx, machines, req); err != nil {
				return err
			}
			return errCanaryRetryRequiresNewOperation
		}
		if !errors.Is(err, provider.ErrNotFound) {
			return err
		}
	}
	if err := removePlatformBootVM(ctx, machines, req); err != nil {
		return fmt.Errorf("reconcile validation VM %s: %w", req.Ref.WorkspaceID, err)
	}
	dir, err := os.MkdirTemp(root, "artifact-boot-")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, os.RemoveAll(dir)) }()
	manifest := artifact.Manifest
	db, _, err := database.OpenBootstrap(ctx, filepath.Join(dir, "boot.db"), database.BootstrapSeed{
		ID: req.Ref.InstallationID, Organization: "Artifact validation", Origin: "https://validation.invalid",
		IDEPattern: "{workspace}.ide.invalid", PreviewPattern: "{service}--{workspace}.preview.invalid",
		Release: manifest.Guest.Version, MinGuestRelease: manifest.Guest.Version, Now: time.Now(),
	})
	if err != nil {
		return err
	}
	defer db.Close()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	cipher, err := cryptostore.New(key)
	clear(key)
	if err != nil {
		return err
	}
	ca, err := EnsureInstallationCA(dir, req.Ref.InstallationID, cipher, time.Now())
	if err != nil {
		return err
	}
	enrollment := guest.NewEnrollment(db, ca)
	singleBoot := artifact.EC2 != nil && artifact.EC2.Family != ""
	if singleBoot {
		if config.EC2Account == "" || config.EC2Region == "" {
			return provider.ErrInvalidState
		}
		config.EC2Bootstrap = guest.NewEC2Bootstrap(enrollment, config.EC2Region, config.EC2Account, time.Now, machines.Inspect)
	}
	listeners, err := ListenGuests(config, enrollment, ca, web.GuestCompatibility{
		Protocol: manifest.ProtocolVersion, CurrentRelease: manifest.Guest.Version,
	}, time.Now)
	if err != nil {
		return err
	}
	defer listeners.Close()
	serveCtx, stop := context.WithCancel(ctx)
	served := make(chan struct{})
	var listenerErr error
	go func() {
		defer close(served)
		listenerErr = listeners.Serve(serveCtx)
		stop()
	}()
	defer func() {
		stop()
		<-served
		if listenerErr != nil && !errors.Is(listenerErr, context.Canceled) && !errors.Is(listenerErr, context.DeadlineExceeded) {
			result = errors.Join(result, listenerErr)
		}
	}()
	// Install cleanup before import so it also handles a lost provider reply.
	defer func() {
		limit := 90 * time.Second
		if artifact.EC2 != nil {
			limit = 4 * time.Minute
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit)
		defer cancel()
		if err := removePlatformBootVM(cleanup, machines, req); err != nil {
			result = errors.Join(result, fmt.Errorf("remove validation VM %s: %w", req.Ref.WorkspaceID, err))
		}
	}()
	var reference string
	if singleBoot {
		var entropy [32]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			return err
		}
		reference = hex.EncodeToString(entropy[:])
		req.BootstrapData, err = enrollment.EC2LaunchData(reference, listeners.EnrollURL, listeners.PollURL)
		if err != nil {
			return err
		}
	}
	vm, err := machines.ImportCandidate(ctx, req, artifact)
	if err != nil {
		return fmt.Errorf("import validation VM: %w", err)
	}
	if err := seedPlatformBoot(ctx, db, req, vm, artifact, reference); err != nil {
		return err
	}
	if singleBoot {
		return waitForPlatformPoll(serveCtx, db, vm.Ref, manifest)
	}
	if _, err := enrollment.Deliver(ctx, req.OperationID, vm, time.Now(), dir, listeners.EnrollURL, listeners.PollURL, machines.DeliverEnrollment); err != nil {
		return fmt.Errorf("deliver validation enrollment: %w", err)
	}
	started, err := machines.Start(ctx, provider.StartRequest{Ref: vm.Ref, OperationID: req.OperationID, NetworkMode: req.NetworkMode})
	if err != nil {
		return fmt.Errorf("start validation VM: %w", err)
	}
	if started.Ref != vm.Ref || started.Power != provider.Running {
		return provider.ErrOutcomeUnknown
	}
	return waitForPlatformPoll(serveCtx, db, vm.Ref, manifest)
}

func seedPlatformBoot(ctx context.Context, db *sql.DB, req provider.CreateRequest, vm provider.Inventory, artifact release.Artifact, reference string) error {
	ref := req.Ref
	ref.InstanceID = vm.InstanceID
	expected := provider.Stopped
	if reference != "" {
		expected = provider.Running
	}
	if vm.Ref != ref || vm.InstanceID == "" || vm.Power != expected {
		return provider.ErrOwnershipMismatch
	}
	const actor = "artifact-validation"
	now := time.Now()
	stamp := now.UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,'validation@example.invalid','administrator','active','local',$1,$2,$2,1)`, actor, stamp); err != nil {
		return err
	}
	artifact.Bundle = filepath.Base(artifact.Bundle)
	req.Resources = vm.Effective
	return seedPlatformEnrollment(ctx, db, req, ref, artifact, actor, now, reference)
}

func seedPlatformEnrollment(ctx context.Context, db *sql.DB, req provider.CreateRequest, ref provider.WorkspaceRef, artifact release.Artifact, actor string, now time.Time, reference string) error {
	store := release.NewStore(db)
	if err := store.BeginImport(ctx, req.ArtifactID, artifact, actor, now); err != nil {
		return err
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	journal := operation.NewJournal(db, time.Now, func() string { return req.OperationID })
	return database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE installations SET state='active' WHERE id=$1`, ref.InstallationID); err != nil {
			return err
		}
		observed, phase := "stopped", "setup.mutate"
		if reference != "" {
			observed, phase = "running", "workspace.launch"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspaces
			(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,repository_url,devcontainer_path,
			 cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,generation,ownership_marker,
			 provider_instance_id,created_at,updated_at,version)
			VALUES($1,$2,$3,'validation',1,$4,'','', $5,$6,$7,'{}','running',$8,$9,$10,$11,$12,$12,1)`,
			ref.WorkspaceID, ref.InstallationID, actor, req.ArtifactID, req.Resources.CPU, req.Resources.MemoryBytes>>20,
			req.Resources.DiskBytes>>20, observed, ref.Generation, ref.OwnershipMarker, ref.InstanceID, stamp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO guest_registrations(workspace_id,generation,state) VALUES($1,$2,'eligible')`, ref.WorkspaceID, ref.Generation); err != nil {
			return err
		}
		op, err := journal.CreateInTx(ctx, tx, operation.Request{Actor: actor, Key: uuid.NewString(), Type: "provision",
			OriginatingAction: phase, WorkspaceID: &ref.WorkspaceID, Generation: ref.Generation,
			Steps: []operation.Step{{Key: "enroll", Label: "Verify platform agent enrollment"}}})
		if err != nil {
			return err
		}
		if reference == "" {
			_, err = tx.ExecContext(ctx, `UPDATE operations SET status='waiting' WHERE id=$1`, op.ID)
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE operations SET status='waiting',current_step='enroll' WHERE id=$1`, op.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE operation_steps SET status='active' WHERE operation_id=$1 AND step_key='enroll'`, op.ID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO ec2_launch_enrollment(operation_id,launch_reference,public_data) VALUES($1,$2,$3)`,
			op.ID, reference, req.BootstrapData)
		return err
	})
}

func waitForPlatformPoll(ctx context.Context, db *sql.DB, ref provider.WorkspaceRef, manifest release.ArtifactManifest) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		var ready bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspaces w JOIN guest_registrations g
			ON g.workspace_id=w.id AND g.generation=w.generation WHERE w.id=$1 AND w.generation=$2 AND w.provider_instance_id=$3
			AND g.state='active' AND g.last_authenticated_at IS NOT NULL AND w.guest_release=$4 AND w.protocol_version=$5)`,
			ref.WorkspaceID, ref.Generation, ref.InstanceID, manifest.Guest.Version, manifest.ProtocolVersion).Scan(&ready); err != nil {
			return err
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for validation agent poll: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func removePlatformBootVM(ctx context.Context, machines PlatformBootProvider, req provider.CreateRequest) error {
	vm, err := machines.Inspect(ctx, req.Ref)
	if errors.Is(err, provider.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	expected := req.Ref
	expected.InstanceID = vm.InstanceID
	if vm.Ref != expected || vm.InstanceID == "" {
		return provider.ErrOwnershipMismatch
	}
	if vm.Power == provider.Running || vm.Power == provider.Starting || vm.Power == provider.Stopping {
		vm, err = machines.Stop(ctx, provider.StopRequest{Ref: expected, OperationID: req.OperationID, ForceRetentionDelete: true})
		if err != nil {
			return err
		}
	}
	if vm.Ref != expected || (vm.Power != provider.Stopped && vm.Power != provider.Deleted) {
		return provider.ErrOutcomeUnknown
	}
	deleted := vm
	if vm.Power != provider.Deleted {
		deleted, err = machines.Delete(ctx, provider.DeleteRequest{Ref: expected, OperationID: req.OperationID})
		if err != nil {
			return err
		}
	}
	if deleted.Ref != expected || deleted.Power != provider.Deleted {
		return provider.ErrOutcomeUnknown
	}
	for _, resource := range deleted.Resources {
		if !resource.Shared && !resource.Immutable {
			return provider.ErrPartial
		}
	}
	return nil
}
