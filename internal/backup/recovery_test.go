package backup

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

const (
	recoveryInstallation = "00000000-0000-4000-8000-000000000901"
	recoveryValidation   = "00000000-0000-4000-8000-000000000902"
	recoveryActor        = "00000000-0000-4000-8000-000000000903"
)

// TestEnterRecoveryInvalidatesRestoredAuthority checks that entering review
// invalidates browser, enrollment, and OAuth credentials copied from backup.
func TestEnterRecoveryInvalidatesRestoredAuthority(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { testEnterRecovery(t, "sqlite", "") })
	t.Run("postgres", func(t *testing.T) {
		uri := os.Getenv("SHIFTBOX_TEST_RECOVERY_GATE_POSTGRES_URI")
		if uri == "" {
			t.Skip("set SHIFTBOX_TEST_RECOVERY_GATE_POSTGRES_URI to a dedicated empty fixture")
		}
		testEnterRecovery(t, "postgres", uri)
	})
}

func testEnterRecovery(t *testing.T, profile, uri string) {
	t.Helper()
	db, journal, now, originalOperation := recoveryProfileFixture(t, profile, uri)
	review, err := EnterRecovery(t.Context(), db, journal, recoveryInstallation, recoveryValidation, recoveryActor, now)
	if err != nil || review == "" {
		t.Fatalf("EnterRecovery for a staged installation = %q, %v; want a review ID", review, err)
	}

	var state, reviewState, restoreOperation string
	err = db.QueryRowContext(t.Context(), `SELECT i.state,r.state,r.restore_operation_id
		FROM installations i CROSS JOIN recovery_reviews r WHERE i.id=$1 AND r.id=$2`, recoveryInstallation, review).
		Scan(&state, &reviewState, &restoreOperation)
	if err != nil || state != "recovery" || reviewState != "identity_review" {
		t.Fatalf("installation and review state = %q, %q, %v; want recovery and identity_review", state, reviewState, err)
	}
	op, err := journal.Get(t.Context(), restoreOperation)
	if err != nil || op.Type != "restore" || op.Actor != recoveryActor {
		t.Errorf("review operation = %q by %q, %v; want restore by the initiating actor", op.Type, op.Actor, err)
	}
	interrupted, err := journal.Get(t.Context(), originalOperation)
	if err != nil || interrupted.Status != "failed" || len(interrupted.Steps) != 1 || interrupted.Steps[0].Status != "failed" {
		t.Errorf("copied workspace operation status = %q, %v; want failed operation and step", interrupted.Status, err)
	}
	var desiredState, observedState string
	err = db.QueryRowContext(t.Context(), "SELECT desired_state,observed_state FROM workspaces WHERE id='fixture-workspace'").
		Scan(&desiredState, &observedState)
	if err != nil || desiredState != "running" || observedState != "running" {
		t.Errorf("retained running workspace state = %q, %q, %v; want no power state change", desiredState, observedState, err)
	}
	assertRestoredAuthority(t, db, false)
	var gitState, accessExpiry, refreshExpiry string
	var nonce, encrypted []byte
	err = db.QueryRowContext(t.Context(), `SELECT state,access_expires_at,refresh_expires_at,nonce,ciphertext
		FROM github_authorizations WHERE user_id=$1 AND app_id='fixture-app'`, recoveryActor).
		Scan(&gitState, &accessExpiry, &refreshExpiry, &nonce, &encrypted)
	if err != nil || gitState != "reconnect_required" || accessExpiry != "" || refreshExpiry != "" || len(nonce) != 0 || len(encrypted) != 0 {
		t.Errorf("restored GitHub authorization = %q with retained credentials %t, %v; want reconnect required without credentials",
			gitState, accessExpiry != "" || refreshExpiry != "" || len(nonce) != 0 || len(encrypted) != 0, err)
	}
}

// TestEnterRecoveryRollsBackOnReviewFailure checks that an interrupted
// transition cannot leave a half-entered recovery gate or partial revocations.
func TestEnterRecoveryRollsBackOnReviewFailure(t *testing.T) {
	db, journal, now, originalOperation := recoveryProfileFixture(t, "sqlite", "")
	_, err := db.ExecContext(t.Context(), `CREATE TRIGGER reject_recovery_review
		BEFORE INSERT ON recovery_reviews BEGIN SELECT RAISE(ABORT,'fixture review failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	review, err := EnterRecovery(t.Context(), db, journal, recoveryInstallation, recoveryValidation, recoveryActor, now)
	if err == nil || review != "" {
		t.Fatalf("EnterRecovery when the review insert fails = %q, %v; want failure with no review ID", review, err)
	}
	var state string
	if err := db.QueryRowContext(t.Context(), "SELECT state FROM installations WHERE id=$1", recoveryInstallation).Scan(&state); err != nil || state != "active" {
		t.Errorf("installation after rollback = %q, %v; want active", state, err)
	}
	var restores int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM operations WHERE type='restore'").Scan(&restores); err != nil || restores != 0 {
		t.Errorf("restore operations after rollback = %d, %v; want none", restores, err)
	}
	original, err := journal.Get(t.Context(), originalOperation)
	if err != nil || original.Status != "pending" {
		t.Errorf("copied workspace operation after rollback = %q, %v; want pending", original.Status, err)
	}
	assertRestoredAuthority(t, db, true)
}

// TestRecoveryBlocksWorkspaceAccessAndDeletionUntilActivation checks that
// exact origin credentials and previously verified retention evidence do
// not bypass recovery, even after the review is ready.
func TestRecoveryBlocksWorkspaceAccessAndDeletionUntilActivation(t *testing.T) {
	ctx := t.Context()
	db, journal, now, originalOperation := recoveryProfileFixture(t, "sqlite", "")
	claim, err := journal.Claim(ctx, originalOperation, "fixture-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.StartStep(ctx, claim, 0); err != nil {
		t.Fatal(err)
	}
	if err := journal.CompleteStep(ctx, claim, 0, "Fixture workspace running"); err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `UPDATE workspaces SET retention_seconds=600,provider_instance_id='fixture-instance'`)
	if err != nil {
		t.Fatal(err)
	}
	inspect := func(_ context.Context, ref provider.WorkspaceRef) (provider.Inventory, error) {
		return provider.Inventory{Ref: ref, InstanceID: ref.InstanceID, Power: provider.Running, NetworkMode: "nat",
			ArtifactID: "fixture-artifact", Release: "fixture", ObservedAt: now}, nil
	}
	limit := int64(3600)
	policy := workspace.RetentionEligibility{MaximumSeconds: &limit, ReconciledAt: now.Add(-time.Hour), OutageGrace: time.Minute}
	store := workspace.NewStore(db)
	candidates, err := store.RetentionCandidates(ctx, recoveryInstallation, now, policy, inspect)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("active installation overdue candidates = %d, %v; want one", len(candidates), err)
	}

	scope := proxy.OriginScope{
		WorkspaceID: "fixture-workspace", Generation: 1, Service: "ide", Origin: "https://fixture.ide.test",
	}
	if _, err := db.ExecContext(ctx, `UPDATE origin_grants SET cookie_digest=$1 WHERE id='restored-origin'`,
		digestBytes(make([]byte, 32))); err != nil {
		t.Fatal(err)
	}
	readCookie := func() error {
		return database.Transaction(ctx, db, func(tx *sql.Tx) error {
			_, err := proxy.ReadOriginCookie(ctx, tx, scope, strings.Repeat("A", 43), now)
			return err
		})
	}
	if err := readCookie(); err != nil {
		t.Fatalf("fixture cookie before recovery = %v; want accepted", err)
	}
	reviewID, err := EnterRecovery(ctx, db, journal, recoveryInstallation, recoveryValidation, recoveryActor, now)
	if err != nil {
		t.Fatal(err)
	}
	change := func(decision RecoveryDecisions) {
		t.Helper()
		if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			_, err := UpdateRecoveryReview(ctx, tx, reviewID, recoveryActor, decision, now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	change(RecoveryDecisions{Version: 1, Section: "identities", UserIDs: []string{recoveryActor}})
	evidence, err := ObserveRecoveryWorkspaces(ctx, db, recoveryInstallation, inspect)
	if err != nil || len(evidence) != 1 || evidence[0].Match != "matched" {
		t.Fatalf("recovery provider evidence = %+v, %v; want confirmed running workspace", evidence, err)
	}
	change(RecoveryDecisions{Version: 2, Section: "provider", Workspaces: evidence})
	change(RecoveryDecisions{Version: 3, Section: "authorization_retention", AcceptRetention: true})

	if err := readCookie(); !errors.Is(err, proxy.ErrOriginGrant) {
		t.Errorf("restored cookie with ready review = %v; want origin grant rejection", err)
	}
	candidate, err := store.RetentionCandidates(ctx, recoveryInstallation, now, policy,
		func(context.Context, provider.WorkspaceRef) (provider.Inventory, error) {
			t.Error("retention inspected provider during recovery")
			return provider.Inventory{}, provider.ErrNotFound
		})
	if err != nil || len(candidate) != 0 {
		t.Errorf("ready review overdue candidates = %d, %v; want none", len(candidate), err)
	}
	if _, err := store.CommitRetentionDeletion(ctx, journal, candidates[0], now, policy); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("retention deletion with pre-recovery evidence = %v; want rejection", err)
	}
	var deletion sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT deletion_commit_at FROM workspaces WHERE id='fixture-workspace'").Scan(&deletion); err != nil || deletion.Valid {
		t.Errorf("retention deletion timestamp during recovery = %+v, %v; want null", deletion, err)
	}

	session := auth.Session{ID: uuid.NewString(), UserID: recoveryActor, Mode: "recovery_review",
		TokenDigest: auth.TokenDigest("review token"), CSRFDigest: auth.TokenDigest("review csrf"), AuthenticatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		users := auth.NewStore(db)
		if err := users.SaveSession(ctx, tx, session); err != nil {
			return err
		}
		principal, err := users.Principal(ctx, tx, "review token")
		if err != nil {
			return err
		}
		principal.RecoveryReady = true
		resource := auth.Resource{Type: "workspace", ID: scope.WorkspaceID, OwnerID: recoveryActor, Eligible: true}
		if !auth.Authorize(principal, "recovery.activate", auth.Resource{}, nil, now).Allowed {
			t.Error("ready review session cannot activate recovery")
		}
		for _, action := range []string{"workspace.connect_owner", "workspace.delete"} {
			if auth.Authorize(principal, action, resource, nil, now).Allowed {
				t.Errorf("ready review session allowed %s before explicit activation", action)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		_, err := ActivateRecoveryInTx(ctx, tx, journal, reviewID, recoveryActor, "explicit-restore-approval", evidence, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := readCookie(); !errors.Is(err, proxy.ErrOriginGrant) {
		t.Errorf("restored cookie after activation = %v; want origin grant rejection", err)
	}
	var installState, reviewState, reason string
	if err := db.QueryRowContext(ctx, `SELECT i.state,r.state,s.revocation_reason FROM installations i
		CROSS JOIN recovery_reviews r CROSS JOIN sessions s WHERE r.id=$1 AND s.id=$2`, reviewID, session.ID).
		Scan(&installState, &reviewState, &reason); err != nil || installState != "active" || reviewState != "activated" || reason != "recovery_activated" {
		t.Errorf("explicit activation = %q / %q / %q, %v; want active installation, activated review, revoked review session",
			installState, reviewState, reason, err)
	}
}

// TestRecoveryInventoryRejectsConflictingEvidence verifies provider lookups
// match the retained instance, ownership, generation, and platform exactly.
func TestRecoveryInventoryRejectsConflictingEvidence(t *testing.T) {
	ctx := t.Context()
	db, journal, now := recoveryFixture(t)
	if _, err := db.ExecContext(ctx, "UPDATE workspaces SET provider_instance_id='recorded-vm'"); err != nil {
		t.Fatal(err)
	}
	if _, err := EnterRecovery(ctx, db, journal, recoveryInstallation, recoveryValidation, recoveryActor, now); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, want string
		change     func(*provider.Inventory)
		inspectErr error
	}{
		{name: "exact match", want: "matched"},
		{name: "missing VM", want: "missing", inspectErr: provider.ErrNotFound},
		{name: "unknown observation", want: "unverified", inspectErr: provider.ErrOutcomeUnknown},
		{name: "different instance", want: "ownership_conflict", change: func(i *provider.Inventory) { i.InstanceID = "different-vm" }},
		{name: "different ownership marker", want: "ownership_conflict", change: func(i *provider.Inventory) { i.Ref.OwnershipMarker = "different-owner" }},
		{name: "different generation", want: "ownership_conflict", change: func(i *provider.Inventory) { i.Ref.Generation++ }},
		{name: "different platform artifact", want: "ownership_conflict", change: func(i *provider.Inventory) { i.ArtifactID = "different-artifact" }},
		{name: "different platform release", want: "ownership_conflict", change: func(i *provider.Inventory) { i.Release = "different-release" }},
		{name: "unconfirmed power", want: "unverified", change: func(i *provider.Inventory) { i.Power = provider.Starting }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed, err := ObserveRecoveryWorkspaces(ctx, db, recoveryInstallation, func(_ context.Context, ref provider.WorkspaceRef) (provider.Inventory, error) {
				inventory := provider.Inventory{Ref: ref, InstanceID: ref.InstanceID, Power: provider.Running,
					ArtifactID: "fixture-artifact", Release: "fixture", ObservedAt: now}
				if tc.change != nil {
					tc.change(&inventory)
				}
				return inventory, tc.inspectErr
			})
			if err != nil || len(observed) != 1 {
				t.Fatalf("provider observations = %+v, %v; want exactly one observation", observed, err)
			}
			if got := observed[0].Match; got != tc.want {
				t.Errorf("provider evidence match = %q; want %q", got, tc.want)
			}
		})
	}
}

func assertRestoredAuthority(t *testing.T, db *sql.DB, intact bool) {
	t.Helper()
	for _, check := range []struct {
		name, query  string
		fixtureCount int
	}{
		{"browser sessions", "SELECT count(*) FROM sessions WHERE revoked_at IS NULL", 1},
		{"origin grants and cookies", "SELECT count(*) FROM origin_grants WHERE revoked_at IS NULL", 1},
		{"enrollment grants", "SELECT count(*) FROM enrollment_grants WHERE state IN ('issued','consumed')", 2},
		{"GitHub OAuth states", "SELECT count(*) FROM github_oauth_states WHERE used=0", 1},
	} {
		want := 0
		if intact {
			want = check.fixtureCount
		}
		var count int
		if err := db.QueryRowContext(t.Context(), check.query).Scan(&count); err != nil || count != want {
			t.Errorf("remaining %s = %d, %v; want %d", check.name, count, err, want)
		}
	}
}

func recoveryFixture(t *testing.T) (*sql.DB, *operation.Journal, time.Time) {
	t.Helper()
	db, journal, now, _ := recoveryProfileFixture(t, "sqlite", "")
	return db, journal, now
}

func recoveryProfileFixture(t *testing.T, profile, uri string) (*sql.DB, *operation.Journal, time.Time, string) {
	t.Helper()
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "recovery.db"),
		PostgresURI: uri, Schema: "shiftbox_test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if profile == "postgres" {
		// Recovery revokes authority across the installation. Run the fixture
		// only in an empty, separately provisioned schema.
		var empty bool
		err := db.QueryRowContext(ctx, `SELECT NOT EXISTS (
			SELECT 1 FROM pg_depend d JOIN pg_namespace n ON d.refobjid=n.oid
			WHERE d.refclassid='pg_namespace'::regclass AND n.nspname=current_schema()
		)`).Scan(&empty)
		if err != nil || !empty {
			t.Fatalf("cannot verify empty PostgreSQL recovery fixture: %v", err)
		}
	}
	if err := database.Migrate(ctx, db, profile); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	before, expiry := now.Add(-time.Hour).Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,
		preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'active','Fixture',$3,'https://management.test','{workspace}.ide.test',
			'{service}--{workspace}.preview.test','fixture','fixture',$2,$2,1)`, recoveryInstallation, before, profile)
	exec(`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,'admin@example.test','administrator','active','local','setup',$2,$2,1)`, recoveryActor, before)
	digest := strings.Repeat("a", 64)
	exec(`INSERT INTO sessions(id,token_digest,user_id,mode,csrf_digest,authenticated_at,expires_at)
		VALUES('restored-session',$1,$2,'normal',$1,$3,$4)`, digest, recoveryActor, before, expiry)
	exec(`INSERT INTO origin_grants(id,token_digest,user_id,session_id,workspace_id,generation,service_key,origin,
		permission_basis,expires_at,consumed_at,cookie_digest,cookie_expires_at)
		VALUES('restored-origin','origin-token',$1,'restored-session','fixture-workspace',1,'ide',
			'https://fixture.ide.test','workspace.connect_owner',$2,$3,'origin-cookie',$2)`, recoveryActor, expiry, before)
	exec(`INSERT INTO github_oauth_states(user_id,app_id,digest,redirect_uri,nonce,ciphertext,expires_at)
		VALUES($1,'fixture-app','oauth-digest','https://management.test/api/v1/git/authorization/callback',$2,$3,$4)`,
		recoveryActor, []byte("fixture nonce"), []byte("fixture ciphertext"), expiry)
	exec(`INSERT INTO github_authorizations(user_id,app_id,provider_user_id,state,nonce,ciphertext,
		access_expires_at,refresh_expires_at,updated_at,version)
		VALUES($1,'fixture-app','fixture-github-user','connected',$2,$3,$4,$4,$5,1)`,
		recoveryActor, []byte("fixture nonce"), []byte("fixture ciphertext"), expiry, before)
	exec(`INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,
		owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
		VALUES('fixture-artifact','fixture','amd64','fixture','fixture','1','[]','{}','{}','{}',$1,'fixture.utm',$2,$3,'available')`,
		digest, recoveryActor, before)
	exec(`INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
		repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,
		observed_state,generation,ownership_marker,created_at,updated_at,version)
		VALUES('fixture-workspace',$1,$2,'fixture-template',1,'fixture-artifact','https://github.com/example/fixture',
			'.devcontainer/devcontainer.json',2,1024,40960,'{"mode":"nat"}','running','running',1,'fixture-marker',$3,$3,1)`,
		recoveryInstallation, recoveryActor, before)
	journal := operation.NewJournal(db, func() time.Time { return now }, uuid.NewString)
	workspace := "fixture-workspace"
	op, err := journal.Create(ctx, operation.Request{Actor: recoveryActor, Key: "fixture-old-provision", Type: "provision",
		OriginatingAction: "workspace.create", WorkspaceID: &workspace, Generation: 1,
		Steps: []operation.Step{{Key: "enroll", Label: "Enroll guest"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO guest_registrations(workspace_id,generation,state) VALUES($1,1,'eligible')", workspace)
	exec(`INSERT INTO enrollment_grants(id,workspace_id,generation,operation_id,provider_instance_id,expires_at,state)
		VALUES($1,$2,1,$3,'fixture-instance',$4,'issued')`, digest, workspace, op.ID, expiry)
	exec(`INSERT INTO enrollment_grants(id,workspace_id,generation,operation_id,provider_instance_id,expires_at,state,key_proof,certificate_der)
		VALUES($1,$2,1,$3,'fixture-instance',$4,'consumed','retained-proof','retained-certificate')`,
		strings.Repeat("b", 64), workspace, op.ID, expiry)
	return db, journal, now, op.ID
}
