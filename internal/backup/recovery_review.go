package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// ErrRecoveryReview signals that a decision does not match the current review.
var ErrRecoveryReview = errors.New("recovery review is incomplete or changed")

type recoveryQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// RecoveryReview is the durable state and evidence for a recovered installation.
type RecoveryReview struct {
	ID                 string           `json:"id"`
	State              string           `json:"state"`
	RestoreOperationID string           `json:"restoreOperationId"`
	Version            int64            `json:"version"`
	Sections           RecoverySections `json:"sections"`
}

// RecoverySections records completed identity and provider review evidence.
type RecoverySections struct {
	UserIDs    []string            `json:"reviewedUserIds"`
	Workspaces []RecoveryWorkspace `json:"workspaces"`
	Identities []RecoveryIdentity  `json:"identities,omitempty"`
}

// RecoveryIdentity is the restored account information an administrator reviews.
type RecoveryIdentity struct {
	ID              string `json:"id"`
	PrimaryEmail    string `json:"primaryEmail"`
	Role            string `json:"role"`
	MembershipState string `json:"membershipState"`
	CredentialMode  string `json:"credentialMode"`
}

// RecoveryWorkspace summarizes the provider's observation of one retained VM.
type RecoveryWorkspace struct {
	WorkspaceID string              `json:"workspaceId"`
	Generation  int64               `json:"generation"`
	Version     int64               `json:"version"`
	Match       string              `json:"match"`
	Power       provider.PowerState `json:"power,omitempty"`
}

// RecoveryDecisions binds a section submission to the reviewed version.
// Workspaces must come from ObserveRecoveryWorkspaces, never from the browser.
type RecoveryDecisions struct {
	Version         int64
	Section         string
	UserIDs         []string
	Workspaces      []RecoveryWorkspace
	AcceptRetention bool
}

// ActivateRecoveryInTx applies a completed review with fresh provider
// evidence, preserving all prior revocations and requiring fresh sign-in.
func ActivateRecoveryInTx(ctx context.Context, tx *sql.Tx, journal *operation.Journal, id, actor, key string,
	fresh []RecoveryWorkspace, now time.Time) (operation.Operation, error) {
	installation, err := lockRecoveryInstallation(ctx, tx, actor)
	if err != nil {
		return operation.Operation{}, err
	}
	review, err := ReadRecoveryReview(ctx, tx, id)
	if err != nil {
		return operation.Operation{}, err
	}
	if now.IsZero() || review.State != "ready" || !slices.Equal(review.Sections.Workspaces, fresh) {
		return operation.Operation{}, ErrRecoveryReview
	}
	current, err := listRecoveryWorkspaces(ctx, tx, installation)
	if err != nil {
		return operation.Operation{}, err
	}
	users, err := recoveryUsers(ctx, tx)
	if err != nil {
		return operation.Operation{}, err
	}
	if !matchesRecoverySet(current, fresh) || !slices.Equal(users, review.Sections.UserIDs) {
		return operation.Operation{}, ErrRecoveryReview
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	for _, ws := range fresh {
		if ws.Match != "matched" || (ws.Power != provider.Running && ws.Power != provider.Stopped) {
			return operation.Operation{}, ErrRecoveryReview
		}
		result, err := tx.ExecContext(ctx, `UPDATE workspaces SET observed_state=$1,updated_at=$2,version=version+1
			WHERE id=$3 AND generation=$4 AND version=$5`, string(ws.Power), stamp, ws.WorkspaceID, ws.Generation, ws.Version)
		if err := changedRecord(result, err); err != nil {
			return operation.Operation{}, err
		}
	}
	op, err := journal.RecordLocalStepInTx(ctx, tx, operation.Request{
		Type: "restore", Actor: actor, Key: key, Generation: 1, OriginatingAction: "recovery.activate",
		Steps: []operation.Step{{Key: "activate", Label: "Activate reviewed installation"}},
	}, "Recovery review approved. Sign in again to continue.")
	if err != nil {
		return operation.Operation{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=$1,revocation_reason='recovery_activated'
		WHERE revoked_at IS NULL`, stamp); err != nil {
		return operation.Operation{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE recovery_reviews SET state='activated',approved_by=$1,activated_at=$2,
		version=version+1 WHERE id=$3 AND state='ready' AND version=$4`, actor, stamp, id, review.Version)
	if err := changedRecord(result, err); err != nil {
		return operation.Operation{}, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE installations SET state='active',updated_at=$1,version=version+1
		WHERE id=$2 AND state='recovery'`, stamp, installation)
	if err := changedRecord(result, err); err != nil {
		return operation.Operation{}, err
	}
	_, err = audit.Append(ctx, tx, audit.Event{OccurredAt: now, ActorType: "user", ActorID: actor,
		Action: "recovery.activate", ResourceType: "recovery", ResourceID: id, Outcome: "activated", ReasonCode: "review_completed"})
	return op, err
}

// ReadRecoveryReview reads a review without changing installation authority.
func ReadRecoveryReview(ctx context.Context, db recoveryQueryer, id string) (RecoveryReview, error) {
	var review RecoveryReview
	var users, workspaces string
	err := db.QueryRowContext(ctx, `SELECT id,state,restore_operation_id,version,reviewed_users_json,reconciled_workspaces_json
		FROM recovery_reviews WHERE id=$1`, id).Scan(&review.ID, &review.State, &review.RestoreOperationID, &review.Version, &users, &workspaces)
	if err != nil {
		return RecoveryReview{}, err
	}
	if err := errors.Join(json.Unmarshal([]byte(users), &review.Sections.UserIDs), json.Unmarshal([]byte(workspaces), &review.Sections.Workspaces)); err != nil {
		return RecoveryReview{}, err
	}
	if review.State == "identity_review" {
		review.Sections.Identities, err = recoveryIdentities(ctx, db)
		if err != nil {
			return RecoveryReview{}, err
		}
	}
	return review, nil
}

// UpdateRecoveryReview records decisions in order while authority stays gated.
func UpdateRecoveryReview(ctx context.Context, tx *sql.Tx, id, actor string, decision RecoveryDecisions, now time.Time) (RecoveryReview, error) {
	installation, err := lockRecoveryInstallation(ctx, tx, actor)
	if err != nil {
		return RecoveryReview{}, err
	}
	review, err := ReadRecoveryReview(ctx, tx, id)
	if err != nil {
		return RecoveryReview{}, err
	}
	if now.IsZero() || review.Version != decision.Version {
		return RecoveryReview{}, ErrRecoveryReview
	}
	switch decision.Section {
	case "identities":
		if review.State != "identity_review" {
			return RecoveryReview{}, ErrRecoveryReview
		}
		users, err := recoveryUsers(ctx, tx)
		if err != nil {
			return RecoveryReview{}, err
		}
		confirmed := slices.Clone(decision.UserIDs)
		slices.Sort(confirmed)
		if !slices.Equal(confirmed, users) {
			return RecoveryReview{}, ErrRecoveryReview
		}
		review.Sections.UserIDs = users
		review.Sections.Identities = nil
		review.State = "provider_reconcile"
	case "provider":
		if review.State != "provider_reconcile" && review.State != "blocked" && review.State != "ready" {
			return RecoveryReview{}, ErrRecoveryReview
		}
		current, err := listRecoveryWorkspaces(ctx, tx, installation)
		if err != nil {
			return RecoveryReview{}, err
		}
		if !matchesRecoverySet(current, decision.Workspaces) {
			return RecoveryReview{}, ErrRecoveryReview
		}
		review.Sections.Workspaces = decision.Workspaces
		review.State = "authorization_review"
		for _, ws := range decision.Workspaces {
			if ws.Match != "matched" {
				review.State = "blocked"
				break
			}
		}
	case "authorization_retention":
		if review.State != "authorization_review" || !decision.AcceptRetention {
			return RecoveryReview{}, ErrRecoveryReview
		}
		review.State = "ready"
	default:
		return RecoveryReview{}, ErrRecoveryReview
	}
	users, err := json.Marshal(review.Sections.UserIDs)
	if err != nil {
		return RecoveryReview{}, err
	}
	workspaces, err := json.Marshal(review.Sections.Workspaces)
	if err != nil {
		return RecoveryReview{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE recovery_reviews SET state=$1,reviewed_users_json=$2,
		reconciled_workspaces_json=$3,version=version+1 WHERE id=$4 AND version=$5`,
		review.State, string(users), string(workspaces), id, decision.Version)
	if err := changedRecord(result, err); err != nil {
		return RecoveryReview{}, err
	}
	_, err = audit.Append(ctx, tx, audit.Event{OccurredAt: now, ActorType: "user", ActorID: actor,
		Action: "recovery.review", ResourceType: "recovery", ResourceID: id,
		Outcome: "recorded", ReasonCode: decision.Section})
	if err != nil {
		return RecoveryReview{}, err
	}
	review.Version++
	return review, nil
}

func lockRecoveryInstallation(ctx context.Context, tx *sql.Tx, actor string) (string, error) {
	var installation string
	err := tx.QueryRowContext(ctx, `UPDATE installations SET version=version WHERE singleton=1 AND state='recovery'
		AND EXISTS (SELECT 1 FROM users WHERE id=$1 AND role='administrator' AND membership_state='active')
		RETURNING id`, actor).Scan(&installation)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrRecoveryReview
	}
	return installation, err
}

func recoveryUsers(ctx context.Context, db recoveryQueryer) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT id FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func recoveryIdentities(ctx context.Context, db recoveryQueryer) ([]RecoveryIdentity, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,primary_email,role,membership_state,credential_mode FROM users ORDER BY primary_email,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var identities []RecoveryIdentity
	for rows.Next() {
		var identity RecoveryIdentity
		if err := rows.Scan(&identity.ID, &identity.PrimaryEmail, &identity.Role, &identity.MembershipState, &identity.CredentialMode); err != nil {
			return nil, err
		}
		identities = append(identities, identity)
	}
	return identities, rows.Err()
}

type recoveryWorkspaceRef struct {
	ref               provider.WorkspaceRef
	version           int64
	artifact, release string
}

func listRecoveryWorkspaces(ctx context.Context, db recoveryQueryer, installation string) ([]recoveryWorkspaceRef, error) {
	rows, err := db.QueryContext(ctx, `SELECT w.id,w.generation,COALESCE(w.provider_instance_id,''),w.ownership_marker,
		w.version,w.platform_artifact_id,a.release_id FROM workspaces w JOIN platform_artifacts a ON a.id=w.platform_artifact_id
		JOIN installations i ON i.id=w.installation_id WHERE w.installation_id=$1 AND i.state='recovery'
		AND w.observed_state<>'deleted' ORDER BY w.id`, installation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	workspaces := []recoveryWorkspaceRef{}
	for rows.Next() {
		ws := recoveryWorkspaceRef{ref: provider.WorkspaceRef{InstallationID: installation}}
		if err := rows.Scan(&ws.ref.WorkspaceID, &ws.ref.Generation, &ws.ref.InstanceID, &ws.ref.OwnershipMarker,
			&ws.version, &ws.artifact, &ws.release); err != nil {
			return nil, err
		}
		workspaces = append(workspaces, ws)
	}
	return workspaces, rows.Err()
}

// ObserveRecoveryWorkspaces inspects retained VMs without making provider
// mutations. The caller runs this outside its authorization transaction.
func ObserveRecoveryWorkspaces(ctx context.Context, db *sql.DB, installation string,
	inspect func(context.Context, provider.WorkspaceRef) (provider.Inventory, error)) ([]RecoveryWorkspace, error) {
	if inspect == nil {
		return nil, ErrRecoveryReview
	}
	workspaces, err := listRecoveryWorkspaces(ctx, db, installation)
	if err != nil {
		return nil, err
	}
	out := make([]RecoveryWorkspace, 0, len(workspaces))
	for _, ws := range workspaces {
		inventory, err := inspect(ctx, ws.ref)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		entry := RecoveryWorkspace{WorkspaceID: ws.ref.WorkspaceID, Version: ws.version, Generation: ws.ref.Generation}
		switch {
		case errors.Is(err, provider.ErrNotFound):
			entry.Match = "missing"
		case errors.Is(err, provider.ErrOwnershipMismatch):
			entry.Match = "ownership_conflict"
		case err != nil:
			entry.Match = "unverified"
		case ws.ref.InstanceID == "" || inventory.Ref != ws.ref || inventory.InstanceID != ws.ref.InstanceID ||
			inventory.ArtifactID != ws.artifact || inventory.Release != ws.release:
			entry.Match = "ownership_conflict"
		case inventory.ObservedAt.IsZero() || (inventory.Power != provider.Running && inventory.Power != provider.Stopped):
			entry.Match = "unverified"
		default:
			entry.Match, entry.Power = "matched", inventory.Power
		}
		out = append(out, entry)
	}
	return out, nil
}

func matchesRecoverySet(current []recoveryWorkspaceRef, observed []RecoveryWorkspace) bool {
	if len(current) != len(observed) {
		return false
	}
	for i, ws := range current {
		if ws.ref.WorkspaceID != observed[i].WorkspaceID || ws.ref.Generation != observed[i].Generation || ws.version != observed[i].Version {
			return false
		}
	}
	return true
}
