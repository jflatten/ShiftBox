package backup

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestReviewRecoveryRequiresOwnershipProof verifies that a conflict blocks
// activation and that a fresh observation can advance the same review.
func TestReviewRecoveryRequiresOwnershipProof(t *testing.T) {
	db, journal, now := recoveryFixture(t)
	ctx := t.Context()
	if _, err := db.ExecContext(ctx, "UPDATE workspaces SET provider_instance_id='recorded-vm'"); err != nil {
		t.Fatal(err)
	}
	id, err := EnterRecovery(ctx, db, journal, recoveryInstallation, recoveryValidation, recoveryActor, now)
	if err != nil {
		t.Fatal(err)
	}
	change := func(decisions RecoveryDecisions) error {
		return database.Transaction(ctx, db, func(tx *sql.Tx) error {
			_, err := UpdateRecoveryReview(ctx, tx, id, recoveryActor, decisions, now)
			return err
		})
	}
	if err := change(RecoveryDecisions{Version: 1, Section: "authorization_retention", AcceptRetention: true}); !errors.Is(err, ErrRecoveryReview) {
		t.Fatalf("out-of-order authorization = %v; want review conflict", err)
	}
	if err := change(RecoveryDecisions{Version: 1, Section: "identities"}); !errors.Is(err, ErrRecoveryReview) {
		t.Fatalf("missing account confirmation = %v; want review conflict", err)
	}
	if err := change(RecoveryDecisions{Version: 1, Section: "identities", UserIDs: []string{recoveryActor}}); err != nil {
		t.Fatal(err)
	}
	observe := func(matching bool) []RecoveryWorkspace {
		t.Helper()
		got, err := ObserveRecoveryWorkspaces(ctx, db, recoveryInstallation, func(_ context.Context, ref provider.WorkspaceRef) (provider.Inventory, error) {
			if !matching {
				return provider.Inventory{}, provider.ErrOwnershipMismatch
			}
			return provider.Inventory{Ref: ref, InstanceID: ref.InstanceID, Power: provider.Running, ArtifactID: "fixture-artifact", Release: "fixture", ObservedAt: now}, nil
		})
		if err != nil || len(got) != 1 {
			t.Fatalf("ObserveRecoveryWorkspaces = %+v, %v; want one observation", got, err)
		}
		return got
	}
	if err := change(RecoveryDecisions{Version: 2, Section: "provider", Workspaces: observe(false)}); err != nil {
		t.Fatal(err)
	}
	if err := change(RecoveryDecisions{Version: 3, Section: "authorization_retention", AcceptRetention: true}); !errors.Is(err, ErrRecoveryReview) {
		t.Fatalf("authorization while blocked = %v; want review conflict", err)
	}
	if err := change(RecoveryDecisions{Version: 3, Section: "provider", Workspaces: observe(true)}); err != nil {
		t.Fatal(err)
	}
	if err := change(RecoveryDecisions{Version: 4, Section: "authorization_retention", AcceptRetention: true}); err != nil {
		t.Fatal(err)
	}
	review, err := ReadRecoveryReview(ctx, db, id)
	if err != nil || review.State != "ready" || review.Version != 5 {
		t.Fatalf("review = %+v, %v; want ready at version 5", review, err)
	}
	var installation string
	if err := db.QueryRowContext(ctx, "SELECT state FROM installations").Scan(&installation); err != nil || installation != "recovery" {
		t.Fatalf("installation after review = %q, %v; want recovery until explicit activation", installation, err)
	}
	approve := func(fresh []RecoveryWorkspace) (operation.Operation, error) {
		var op operation.Operation
		err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			var err error
			op, err = ActivateRecoveryInTx(ctx, tx, journal, id, recoveryActor, "recovery-activate-fixture", fresh, now)
			return err
		})
		return op, err
	}
	if _, err := approve(observe(false)); !errors.Is(err, ErrRecoveryReview) {
		t.Fatalf("activation after provider conflict = %v; want rejection", err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE workspaces SET version=version+1"); err != nil {
		t.Fatal(err)
	}
	if _, err := approve(observe(true)); !errors.Is(err, ErrRecoveryReview) {
		t.Fatalf("activation after workspace changed = %v; want rejection", err)
	}
	if err := change(RecoveryDecisions{Version: 5, Section: "provider", Workspaces: observe(true)}); err != nil {
		t.Fatal(err)
	}
	if err := change(RecoveryDecisions{Version: 6, Section: "authorization_retention", AcceptRetention: true}); err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{
		User:              auth.User{ID: recoveryActor, Role: "administrator", MembershipState: "active", CredentialMode: "local"},
		Session:           auth.Session{UserID: recoveryActor, Mode: "recovery_review", ExpiresAt: now.AddDate(0, 0, 1)},
		InstallationState: "recovery", RecoveryReady: true,
	}
	for _, action := range []string{"workspace.start", "workspace.connect_owner", "workspace.delete", "user.create"} {
		if auth.Authorize(principal, action, auth.Resource{Type: "workspace", OwnerID: recoveryActor}, nil, now).Allowed {
			t.Errorf("recovery session gained normal authority for %s", action)
		}
	}
	op, err := approve(observe(true))
	if err != nil || op.Status != "succeeded" {
		t.Fatalf("explicit approval = %s, %v; want succeeded", op.Status, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT state FROM installations").Scan(&installation); err != nil || installation != "active" {
		t.Fatalf("installation after approval = %q, %v; want active", installation, err)
	}
	assertRestoredAuthority(t, db, false)
}
