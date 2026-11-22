package app

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/identity"
)

const (
	builtAuthorityInstallation = "99999999-0000-4000-8000-000000000009"
	builtAuthorityRepository   = "https://github.com/example/project"
	builtAuthorityConfig       = ".devcontainer/devcontainer.json"
	builtAuthorityOwner        = "cache-guest-owner"
	builtAuthorityApp          = "fixture-github-app"
	builtAuthorityStamp        = "2026-11-20T17:00:00.000000000Z"
	builtAuthorityDispatch     = int64(7)
	builtAuthorityGeneration   = int64(2)
)

// TestBuiltImageAuthority derives scope from the authenticated workspace and
// fences an existing grant when any current source or local authority changes.
func TestBuiltImageAuthority(t *testing.T) {
	db := seedBuiltImageAuthority(t)
	ctx := t.Context()
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	github, err := identity.NewGitHub(db, cipher, builtAuthorityApp, "fixture-app-secret", 1, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	contact := guest.Contact{
		WorkspaceID: "workspace-first", OwnerID: builtAuthorityOwner, Repository: builtAuthorityRepository,
		Generation: builtAuthorityGeneration, DesiredState: "running",
	}
	const operation = "launch-first"
	begin := func(t *testing.T) *sql.Tx {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin authority fixture transaction: %v", err)
		}
		t.Cleanup(func() { _ = tx.Rollback() })
		return tx
	}
	load := func(t *testing.T, tx *sql.Tx) builtImageAuthority {
		t.Helper()
		authority, err := loadBuiltImageAuthority(ctx, tx, contact, operation, builtAuthorityDispatch, false)
		if err != nil {
			t.Fatalf("loadBuiltImageAuthority(%s, %s, %d) = %v, want current authority", contact.WorkspaceID, operation, builtAuthorityDispatch, err)
		}
		return authority
	}
	requireDenied := func(t *testing.T, tx *sql.Tx, current guest.Contact, operationID string, dispatchVersion int64, allowSucceeded bool) {
		t.Helper()
		if _, err := loadBuiltImageAuthority(ctx, tx, current, operationID, dispatchVersion, allowSucceeded); err == nil {
			t.Errorf("loadBuiltImageAuthority(%s generation %d, %s dispatch %d, allowSucceeded=%t) succeeded, want denial",
				current.WorkspaceID, current.Generation, operationID, dispatchVersion, allowSucceeded)
		}
	}

	// A workspace can use its current project scope even when two projects share
	// the same Git source, but neither scope is valid for the other's grant.
	t.Run("identical source in a second project", func(t *testing.T) {
		tx := begin(t)
		first := load(t, tx)
		if first.projectID != "project-first" || first.workspaceID != contact.WorkspaceID || first.ownerID != builtAuthorityOwner ||
			first.installationID != builtAuthorityInstallation || first.generation != builtAuthorityGeneration || first.policyGeneration != 1 ||
			first.projectRevision != 2 || first.repositoryURL != builtAuthorityRepository || first.repositoryRef != "main" ||
			first.configPath != builtAuthorityConfig || first.targetOS != "linux" || first.targetArch != "amd64" {
			t.Fatalf("loadBuiltImageAuthority(%s) = %+v, want current project, workspace, origin, linux/amd64, and generation 2",
				contact.WorkspaceID, first)
		}
		other := contact
		other.WorkspaceID = "workspace-second"
		second, err := loadBuiltImageAuthority(ctx, tx, other, "launch-second", builtAuthorityDispatch, false)
		if err != nil || second.projectID != "project-second" || second.repositoryURL != first.repositoryURL || second.configPath != first.configPath {
			t.Fatalf("loadBuiltImageAuthority(%s) = %+v, %v, want an independent project with identical source", other.WorkspaceID, second, err)
		}
		if err := validateBuiltImageAuthority(first, first, 4, 4); err != nil {
			t.Fatalf("validateBuiltImageAuthority(current grant, current grant, 4, 4) = %v, want success", err)
		}
		if err := validateBuiltImageAuthority(second, first, 4, 4); err == nil {
			t.Error("validateBuiltImageAuthority(second project, first project's grant, 4, 4) succeeded, want denial")
		}
		requireDenied(t, tx, other, operation, builtAuthorityDispatch, false)
	})

	// Disabled users, revoked enrollment, and revoked template sharing each
	// invalidate an already-authenticated contact before an upload can finish.
	for _, test := range []struct {
		name  string
		query string
	}{
		{name: "membership disabled", query: `UPDATE users SET membership_state='disabled' WHERE id='cache-guest-owner'`},
		{name: "enrollment revoked", query: `UPDATE guest_registrations SET state='revoked' WHERE workspace_id='workspace-first'`},
		{name: "project access revoked", query: `UPDATE template_grants SET revoked_at='2026-11-20T18:00:00Z' WHERE template_id='project-first'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := begin(t)
			load(t, tx)
			builtAuthorityExec(t, ctx, tx, test.query)
			requireDenied(t, tx, contact, operation, builtAuthorityDispatch, true)
		})
	}

	// Disconnecting through the GitHub service revokes the stored source
	// authorization and leaves the original grant unusable.
	t.Run("source access revoked", func(t *testing.T) {
		tx := begin(t)
		current := load(t, tx)
		source, err := github.AuthorizationViewInTx(ctx, tx, current.ownerID)
		if err != nil || source.State != "connected" || source.Version != 4 {
			t.Fatalf("AuthorizationViewInTx(%s) = %+v, %v, want connected version 4", current.ownerID, source, err)
		}
		if err := github.RevokeAuthorizationInTx(ctx, tx, current.ownerID); err != nil {
			t.Fatal(err)
		}
		revoked, err := github.AuthorizationViewInTx(ctx, tx, current.ownerID)
		if err != nil || revoked.State != "revoked" || revoked.Version != 5 {
			t.Fatalf("AuthorizationViewInTx(%s) after disconnect = %+v, %v, want revoked version 5", current.ownerID, revoked, err)
		}
		if err := validateBuiltImageAuthority(current, current, 0, source.Version); err == nil {
			t.Error("validateBuiltImageAuthority(revoked source version, admitted source version) succeeded, want denial")
		}
		if err := validateBuiltImageAuthority(current, current, revoked.Version, source.Version); err == nil {
			t.Error("validateBuiltImageAuthority(changed source version, admitted source version) succeeded, want denial")
		}
	})

	// Re-enablement cannot restore earlier policy grants, and a new dispatch or
	// workspace generation cannot resume a transfer from stale guest authority.
	t.Run("generation and dispatch fencing", func(t *testing.T) {
		tx := begin(t)
		old := load(t, tx)
		builtAuthorityExec(t, ctx, tx, `UPDATE installations SET built_image_generation=3,built_image_version=3,
			built_image_purge_cutoff=1 WHERE id=$1`, builtAuthorityInstallation)
		current := load(t, tx)
		if current.policyGeneration != 3 {
			t.Fatalf("loadBuiltImageAuthority(%s) policy generation = %d, want 3", contact.WorkspaceID, current.policyGeneration)
		}
		if err := validateBuiltImageAuthority(current, old, 4, 4); err == nil {
			t.Error("validateBuiltImageAuthority(current policy generation, earlier grant, 4, 4) succeeded, want denial")
		}
		stale := contact
		stale.Generation = 1
		requireDenied(t, tx, stale, operation, builtAuthorityDispatch, true)
		builtAuthorityExec(t, ctx, tx, "UPDATE guest_dispatches SET dispatch_version=8 WHERE operation_id=$1", operation)
		requireDenied(t, tx, contact, operation, builtAuthorityDispatch, true)
	})

	// An eligible background upload retains its original authority after the
	// workspace succeeds. A completed launch cannot start a new lookup.
	t.Run("eligible upload finishes after launch success", func(t *testing.T) {
		tx := begin(t)
		admitted := load(t, tx)
		builtAuthorityExec(t, ctx, tx, `UPDATE guest_dispatches SET status='succeeded',container_id=$1 WHERE operation_id=$2`,
			strings.Repeat("b", 64), operation)
		builtAuthorityExec(t, ctx, tx, `UPDATE operation_steps SET status='succeeded',finished_at=$1 WHERE operation_id=$2`,
			builtAuthorityStamp, operation)
		builtAuthorityExec(t, ctx, tx, `UPDATE operations SET status='succeeded',finished_at=$1,current_step=NULL WHERE id=$2`,
			builtAuthorityStamp, operation)
		requireDenied(t, tx, contact, operation, builtAuthorityDispatch, false)
		finished, err := loadBuiltImageAuthority(ctx, tx, contact, operation, builtAuthorityDispatch, true)
		if err != nil {
			t.Fatalf("loadBuiltImageAuthority(%s, succeeded dispatch %d) = %v, want upload to continue", operation, builtAuthorityDispatch, err)
		}
		if err := validateBuiltImageAuthority(finished, admitted, 4, 4); err != nil {
			t.Errorf("validateBuiltImageAuthority(succeeded launch, admitted upload, 4, 4) = %v, want success", err)
		}
	})
}

// seedBuiltImageAuthority creates two real project/guest/dispatch paths with
// the same current config and a member who relies on explicit launch grants.
func seedBuiltImageAuthority(t *testing.T) *sql.DB {
	t.Helper()
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "built-authority.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatalf("Migrate(sqlite) = %v, want success", err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		builtAuthorityExec(t, ctx, tx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,
			ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
			VALUES($1,'active','ShiftBox','sqlite','https://management.test','{workspace}.ide.test',
			'{service}--{workspace}.preview.test','fixture','fixture',$2,$2,1)`, builtAuthorityInstallation, builtAuthorityStamp)
		builtAuthorityExec(t, ctx, tx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
			VALUES('cache-project-owner','project-owner@example.test','member','active','local','setup',$2,$2,1),
			($1,'guest-owner@example.test','member','active','local','setup',$2,$2,1)`, builtAuthorityOwner, builtAuthorityStamp)
		builtAuthorityExec(t, ctx, tx, `INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,
			owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
			VALUES('cache-artifact','fixture','amd64','test','fixture','1','[]','{}','{}','{}',$1,'fixture.utm','cache-project-owner',$2,'available')`,
			strings.Repeat("a", 64), builtAuthorityStamp)
		builtAuthorityExec(t, ctx, tx, `INSERT INTO resource_profiles(id,name,cpu_count,memory_mib,disk_mib,enabled,version)
			VALUES('cache-profile','Cache fixture',2,1024,40,1,1)`)
		builtAuthorityExec(t, ctx, tx, `INSERT INTO github_authorizations(user_id,app_id,provider_user_id,state,nonce,ciphertext,
			access_expires_at,refresh_expires_at,updated_at,version) VALUES($1,$2,'github-member','connected',$3,$3,'','',$4,4)`,
			builtAuthorityOwner, builtAuthorityApp, []byte{}, builtAuthorityStamp)

		for _, suffix := range []string{"first", "second"} {
			project, workspace, operation := "project-"+suffix, "workspace-"+suffix, "launch-"+suffix
			builtAuthorityExec(t, ctx, tx, `INSERT INTO project_templates(id,owner_user_id,name,state,current_revision,created_at,updated_at,version)
				VALUES($1,'cache-project-owner',$1,'active',2,$2,$2,2)`, project, builtAuthorityStamp)
			builtAuthorityExec(t, ctx, tx, `INSERT INTO template_revisions(template_id,revision,repository_url,repository_ref,
				devcontainer_path,resource_profile_id,platform_artifact_id,secret_names_json,created_by,created_at)
				VALUES($1,1,$2,'previous',$3,'cache-profile','cache-artifact','[]','cache-project-owner',$4),
				($1,2,$2,'main',$3,'cache-profile','cache-artifact','[]','cache-project-owner',$4)`,
				project, builtAuthorityRepository, builtAuthorityConfig, builtAuthorityStamp)
			builtAuthorityExec(t, ctx, tx, `INSERT INTO template_grants(template_id,user_id,can_launch,project_secret_warning_ack_at,granted_by,granted_at)
				VALUES($1,$2,1,$3,'cache-project-owner',$3)`, project, builtAuthorityOwner, builtAuthorityStamp)
			builtAuthorityExec(t, ctx, tx, `INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
				repository_url,repository_ref,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,
				observed_state,generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
				VALUES($1,$2,$3,$4,1,'cache-artifact',$5,'previous',$6,2,1024,40,'{"mode":"shared"}','running',
				'running',$7,'cache-fixture',$1,$8,$8,1)`, workspace, builtAuthorityInstallation, builtAuthorityOwner,
				project, builtAuthorityRepository, builtAuthorityConfig, builtAuthorityGeneration, builtAuthorityStamp)
			builtAuthorityExec(t, ctx, tx, `INSERT INTO guest_registrations(workspace_id,generation,state,certificate_serial,
				certificate_not_after,public_key_fingerprint) VALUES($1,$2,'active','01','2099-11-21T01:00:00Z',$3)`,
				workspace, builtAuthorityGeneration, strings.Repeat("a", 64))
			builtAuthorityExec(t, ctx, tx, `INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,current_step,
				created_at,started_at,version,originating_action) VALUES($1,$2,'provision',$3,$4,$1,'running','configure',$5,$5,7,'workspace.launch')`,
				operation, workspace, builtAuthorityGeneration, builtAuthorityOwner, builtAuthorityStamp)
			builtAuthorityExec(t, ctx, tx, `INSERT INTO operation_steps(operation_id,step_index,step_key,label,status,started_at)
				VALUES($1,0,'configure','Configure workspace','active',$2)`, operation, builtAuthorityStamp)
			builtAuthorityExec(t, ctx, tx, `INSERT INTO guest_dispatches(operation_id,dispatch_version,status) VALUES($1,$2,'running')`,
				operation, builtAuthorityDispatch)
		}
		return nil
	}); err != nil {
		t.Fatalf("commit built-image authority fixture: %v", err)
	}
	return db
}

func builtAuthorityExec(t *testing.T, ctx context.Context, tx *sql.Tx, query string, args ...any) {
	t.Helper()
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("ExecContext(%s, %v) = %v, want success", query, args, err)
	}
}
