package backup

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
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
	exec(`INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,
		owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
		VALUES('fixture-artifact','fixture','amd64','fixture','fixture','1','[]','{}','{}','{}',$1,'fixture.utm',$2,$3,'available')`,
		digest, recoveryActor, before)
	exec(`INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
		repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,
		observed_state,generation,ownership_marker,created_at,updated_at,version)
		VALUES('fixture-workspace',$1,$2,'fixture-template',1,'fixture-artifact','https://github.com/example/fixture',
			'.devcontainer/devcontainer.json',2,1024,40960,'{}','running','running',1,'fixture-marker',$3,$3,1)`,
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
