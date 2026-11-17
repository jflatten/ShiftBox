package guest_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/secret"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspace"
	_ "modernc.org/sqlite"
)

// TestEnrollmentGrantReplay verifies one-use issuance, lost-response recovery
// for the same key, and denial after expiry or registration revocation.
func TestEnrollmentGrantReplay(t *testing.T) {
	ctx := context.Background()
	cfg := database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "guest.db")}
	if uri := os.Getenv("SHIFTBOX_TEST_GUEST_POSTGRES_URI"); uri != "" {
		cfg = database.Config{Profile: "postgres", PostgresURI: uri, Schema: "shiftbox_test"}
	}
	db, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, cfg.Profile); err != nil {
		t.Fatal(err)
	}
	const install = "00000000-0000-4000-8000-000000000001"
	_, err = db.Exec(`
	INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
	VALUES('` + install + `','active','ShiftBox','` + cfg.Profile + `','https://management.test','ide.test','preview.test','release','release','now','now',1);
	INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
	VALUES('owner','a@example.test','member','active','local','setup','now','now',1);
	INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
	VALUES('artifact','release','amd64','test','test','1','[]','{}','{}','{}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture.utm','owner','now','available');
	INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
	VALUES('work','` + install + `','owner','template',1,'artifact','https://github.com/example/test','devcontainer.json',2,1024,40,'{}','running','stopped',1,'marker','vm','now','now',1);
	INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,created_at,version)
	VALUES('op','work','provision',1,'owner','fixture-enrollment','running','now',1);
	INSERT INTO guest_registrations(workspace_id,generation,state) VALUES('work',1,'eligible');`)
	if err != nil {
		t.Fatal(err)
	}
	// A retained workspace without any accepted poll blocks advancement.
	store := release.NewStore(db)
	blockers, err := store.GuestUpgradeBlockers(ctx, install, "test", "1")
	if err != nil || len(blockers) != 1 || blockers[0] != (release.GuestUpgradeBlocker{
		WorkspaceID: "work", ObservedState: "stopped",
	}) {
		t.Errorf("guest upgrade blockers before enrollment = %+v, %v, want stopped guest without release evidence", blockers, err)
	}
	certPEM, keyPEM, issuerKey, issuer := authorityFixture(t)
	now := time.Now().UTC()
	ca, err := guest.LoadCA(certPEM, keyPEM, now)
	if err != nil {
		t.Fatal(err)
	}
	enrollment := guest.NewEnrollment(db, ca)
	ref := provider.WorkspaceRef{InstallationID: install, WorkspaceID: "work", Generation: 1, OwnershipMarker: "marker", InstanceID: "vm"}
	inventory := provider.Inventory{Ref: ref, InstanceID: "vm", Power: provider.Stopped, ObservedAt: now}
	wrong := inventory
	wrong.Ref.OwnershipMarker = "foreign"
	if _, err := enrollment.Mint(ctx, "op", wrong, now); !errors.Is(err, guest.ErrEnrollmentDenied) {
		t.Errorf("mint for mismatched ownership = %v, want denial", err)
	}
	wrong = inventory
	wrong.Power = provider.Running
	if _, err := enrollment.Mint(ctx, "op", wrong, now); !errors.Is(err, guest.ErrEnrollmentDenied) {
		t.Errorf("mint for running VM = %v, want denial", err)
	}
	token, err := enrollment.Mint(ctx, "op", inventory, now)
	if err != nil {
		t.Fatal(err)
	}
	var mediaRoot, retainedImage string
	if os.Getenv("SHIFTBOX_TEST_ENROLLMENT_MEDIA") != "" {
		root := t.TempDir()
		mediaRoot = root
		failure := errors.New("provider lost delivery reply")
		_, err := enrollment.Deliver(ctx, "op", inventory, now, root, "https://internal.test/internal/v1/enroll", "https://internal.test:8443/internal/v1/guest/poll",
			func(_ context.Context, req provider.EnrollmentRequest) (provider.DeliveryResult, error) {
				if req.Ref != ref || req.OperationID != "op" || !req.Eligible {
					t.Error("delivery did not use the verified operation and instance")
				}
				if _, err := os.Stat(req.MediaPath); err != nil {
					t.Errorf("delivery image unavailable to provider: %v", err)
				}
				return provider.DeliveryResult{}, failure
			})
		if !errors.Is(err, failure) {
			t.Fatalf("lost media delivery = %v, want provider error", err)
		}
		if files, err := os.ReadDir(root); err != nil || len(files) != 0 {
			t.Fatalf("invalidated attempt left files: %v, %v", files, err)
		}
		var issued int
		if err := db.QueryRow("SELECT COUNT(*) FROM enrollment_grants WHERE state='issued'").Scan(&issued); err != nil || issued != 0 {
			t.Fatalf("issued grants after failed delivery = %d, %v", issued, err)
		}
		if _, err := db.Exec("DELETE FROM enrollment_grants WHERE state='invalidated'"); err != nil {
			t.Fatal(err)
		}
		image, err := enrollment.Deliver(ctx, "op", inventory, now, root, "https://internal.test/internal/v1/enroll", "https://internal.test:8443/internal/v1/guest/poll",
			func(_ context.Context, req provider.EnrollmentRequest) (provider.DeliveryResult, error) {
				data, err := os.ReadFile(req.MediaPath)
				if err != nil {
					return provider.DeliveryResult{}, err
				}
				field := []byte(`"grant":"`)
				start := bytes.Index(data, field)
				if start < 0 || start+len(field)+43 > len(data) {
					t.Fatal("delivery media omitted the grant")
				}
				token = string(data[start+len(field) : start+len(field)+43])
				return provider.DeliveryResult{Inventory: inventory, MediaID: "vm/recovery"}, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(image); err != nil {
			t.Fatalf("successful delivery did not retain the image: %v", err)
		}
		retainedImage = image
		reopened := guest.NewEnrollment(db, ca)
		if _, err := db.Exec("UPDATE enrollment_grants SET media_attached_at=NULL WHERE state='issued'"); err != nil {
			t.Fatal(err)
		}
		resumed, err := reopened.Deliver(ctx, "op", inventory, now, root, "https://internal.test/internal/v1/enroll", "https://internal.test:8443/internal/v1/guest/poll",
			func(_ context.Context, req provider.EnrollmentRequest) (provider.DeliveryResult, error) {
				if req.MediaPath != image {
					t.Fatal("interrupted delivery replaced the prepared image")
				}
				return provider.DeliveryResult{Inventory: inventory, MediaID: "vm/recovery"}, nil
			})
		if err != nil || resumed != image {
			t.Fatalf("interrupted delivery = %q, %v, want original prepared image", resumed, err)
		}
		resumed, err = reopened.Deliver(ctx, "op", inventory, now, root, "https://internal.test/internal/v1/enroll", "https://internal.test:8443/internal/v1/guest/poll",
			func(context.Context, provider.EnrollmentRequest) (provider.DeliveryResult, error) {
				t.Fatal("confirmed delivery reached the provider again")
				return provider.DeliveryResult{}, nil
			})
		if err != nil || resumed != image {
			t.Fatalf("reopened delivery = %q, %v, want original attachment", resumed, err)
		}
		if err := reopened.CleanupDelivery(ctx, "op", ref, root); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(image); err != nil {
			t.Fatal("cleanup removed the unconsumed image")
		}
	}
	var digest string
	if err := db.QueryRow("SELECT id FROM enrollment_grants").Scan(&digest); err != nil || len(digest) != 64 || digest == token {
		t.Fatalf("stored grant ID length = %d, %v, want digest", len(digest), err)
	}
	csr, clientKey := fixtureCSRWithKey(t)
	first := enrollOverTLS(t, ca, leafFixture(t, issuerKey, issuer, false), enrollment, token, csr, now)
	leaf, err := x509.ParseCertificate(first)
	if err != nil {
		t.Fatal(err)
	}
	// Download authority follows the current registration and the exact
	// workspace generation's active staging step.
	t.Run("maintenance download authority", func(t *testing.T) {
		const campaign, target = "fixture-update-campaign", "99999999-0000-4000-8000-000000000002"
		if _, err := enrollment.UpdateRelease(ctx, leaf, now); !errors.Is(err, guest.ErrGuestUnauthorized) {
			t.Errorf("UpdateRelease without an assigned operation = %v, want denial", err)
		}
		defer func() {
			for _, query := range []string{
				"UPDATE installations SET active_release='release' WHERE id='00000000-0000-4000-8000-000000000001'",
				"DELETE FROM maintenance_targets WHERE campaign_id='fixture-update-campaign'",
				"DELETE FROM maintenance_campaigns WHERE operation_id='fixture-update-campaign'",
				"DELETE FROM guest_dispatches WHERE operation_id='99999999-0000-4000-8000-000000000002'",
				"DELETE FROM operation_steps WHERE operation_id='99999999-0000-4000-8000-000000000002'",
				"DELETE FROM operations WHERE id IN ('fixture-update-campaign','99999999-0000-4000-8000-000000000002')",
			} {
				if _, err := db.Exec(query); err != nil {
					t.Error(err)
				}
			}
		}()
		for _, query := range []string{
			"UPDATE installations SET active_release='test' WHERE id='00000000-0000-4000-8000-000000000001'",
			`INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,current_step,created_at,version,originating_action) VALUES
			('fixture-update-campaign',NULL,'update',1,'owner','campaign-fixture','waiting','update_guests','now',1,'maintenance.manage'),
			('99999999-0000-4000-8000-000000000002','work','update',1,'controller','target-fixture','waiting','maintenance_stage','now',1,'maintenance.update_guest')`,
			`INSERT INTO operation_steps(operation_id,step_index,step_key,label,status)
			VALUES('99999999-0000-4000-8000-000000000002',0,'maintenance_stage','Stage update','active')`,
			`INSERT INTO maintenance_campaigns(operation_id,installation_id,release_id,deadline,created_by,created_at,state)
			SELECT 'fixture-update-campaign',id,'release','now','owner','now','active' FROM installations`,
			`INSERT INTO maintenance_targets(campaign_id,workspace_id,generation,prior_desired_state,prior_observed_state,operation_id,state,updated_at)
			VALUES('fixture-update-campaign','work',1,'running','running','99999999-0000-4000-8000-000000000002','waiting_safe','now')`,
		} {
			if _, err := db.Exec(query); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := enrollment.UpdateRelease(ctx, leaf, now); err != nil || got != "test" {
			t.Errorf("UpdateRelease during staging = %q, %v, want assigned release", got, err)
		}
		input := guest.PollReport{GuestRelease: "test", ProtocolVersion: "1"}
		_, dispatch, err := enrollment.Exchange(ctx, leaf, now, input)
		if err != nil || dispatch == nil || dispatch.ID != target || dispatch.Command != "stage-guest-update" || dispatch.Arguments.ProductRelease != "test" {
			t.Fatalf("maintenance dispatch = %+v (%v), want assigned staging command", dispatch, err)
		}
		receipt := &guest.OperationReport{ID: target, Generation: 1, Status: "succeeded", Step: "update.stage", Attempt: 1}
		receipt.Evidence.UpdateDigest = strings.Repeat("a", 64)
		input.LastOperationID, input.Version, input.Operation = target, dispatch.Version, receipt
		for range 2 {
			if _, next, err := enrollment.Exchange(ctx, leaf, now, input); err != nil || next != nil {
				t.Fatalf("staging receipt = %+v (%v), want successful idempotent receipt", next, err)
			}
		}
		receipt.Evidence.UpdateDigest = strings.Repeat("b", 64)
		if _, _, err := enrollment.Exchange(ctx, leaf, now, input); !errors.Is(err, guest.ErrGuestReportConflict) {
			t.Errorf("changed staging receipt = %v, want conflict", err)
		}
		if _, err := db.Exec(`UPDATE operations SET current_step='maintenance_activate' WHERE id=$1`, target); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO operation_steps(operation_id,step_index,step_key,label,status)
			VALUES($1,1,'maintenance_activate','Activate update','active')`, target); err != nil {
			t.Fatal(err)
		}
		if _, err := enrollment.UpdateRelease(ctx, leaf, now); !errors.Is(err, guest.ErrGuestUnauthorized) {
			t.Errorf("UpdateRelease after staging = %v, want denial", err)
		}
		// The guest can replay its staging receipt while the trial boots.
		stagedDigest := strings.Repeat("a", 64)
		receipt.Evidence.UpdateDigest = stagedDigest
		_, activation, err := enrollment.Exchange(ctx, leaf, now, input)
		if err != nil || activation == nil || activation.Command != "activate-guest-update" || activation.Version != dispatch.Version {
			t.Fatalf("activation dispatch = %+v (%v), want activation for staged version", activation, err)
		}
		receipt.Step = "update.activate"
		receipt.Evidence.UpdateDigest = strings.Repeat("b", 64)
		if _, _, err := enrollment.Exchange(ctx, leaf, now, input); !errors.Is(err, guest.ErrGuestReportConflict) {
			t.Errorf("activation for a different digest = %v, want conflict", err)
		}
		receipt.Evidence.UpdateDigest = stagedDigest
		input.GuestRelease = "previous"
		if _, _, err := enrollment.Exchange(ctx, leaf, now, input); !errors.Is(err, guest.ErrGuestReportConflict) {
			t.Errorf("activation from the previous release = %v, want conflict", err)
		}
		input.GuestRelease = "test"
		for range 2 {
			if _, next, err := enrollment.Exchange(ctx, leaf, now, input); err != nil || next != nil {
				t.Fatalf("activation receipt = %+v (%v), want successful idempotent receipt", next, err)
			}
		}
		var status, savedDigest string
		if err := db.QueryRow(`SELECT activation_status,update_digest FROM guest_dispatches WHERE operation_id=$1`,
			target).Scan(&status, &savedDigest); err != nil || status != "succeeded" || savedDigest != stagedDigest {
			t.Errorf("stored activation = %q/%q (%v), want succeeded with staged digest", status, savedDigest, err)
		}
		receipt.Status, receipt.Problem = "failed", "guest.update_rolled_back"
		if _, _, err := enrollment.Exchange(ctx, leaf, now, input); !errors.Is(err, guest.ErrGuestReportConflict) {
			t.Errorf("changed activation receipt = %v, want conflict", err)
		}
		// A maintenance boot can recover a retained expired identity while
		// desired state stays stopped. Leaving boot closes that authority.
		defer func() {
			if _, err := db.Exec("UPDATE workspaces SET desired_state='running' WHERE id='work'"); err != nil {
				t.Error(err)
			}
			if _, err := db.Exec("UPDATE guest_registrations SET state='active' WHERE workspace_id='work'"); err != nil {
				t.Error(err)
			}
			if _, err := db.Exec("DELETE FROM enrollment_grants WHERE operation_id=$1", target); err != nil {
				t.Error(err)
			}
			if _, err := db.Exec("UPDATE enrollment_grants SET state='consumed' WHERE id=$1 AND operation_id='op'", digest); err != nil {
				t.Error(err)
			}
		}()
		for _, query := range []string{
			"UPDATE workspaces SET desired_state='stopped' WHERE id='work'",
			"UPDATE guest_registrations SET state='expired' WHERE workspace_id='work'",
		} {
			if _, err := db.Exec(query); err != nil {
				t.Fatal(err)
			}
		}
		inventory := provider.Inventory{Ref: ref, InstanceID: ref.InstanceID, Power: provider.Stopped, ObservedAt: now}
		if _, err := enrollment.Mint(ctx, target, inventory, now); !errors.Is(err, guest.ErrEnrollmentDenied) {
			t.Errorf("recovery grant during activation = %v, want denial", err)
		}
		if _, err := db.Exec("UPDATE maintenance_targets SET prior_desired_state='stopped' WHERE operation_id=$1", target); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("UPDATE operations SET current_step='maintenance_boot' WHERE id=$1", target); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO operation_steps(operation_id,step_index,step_key,label,status)
			VALUES($1,2,'maintenance_boot','Management boot','active')`, target); err != nil {
			t.Fatal(err)
		}
		if _, err := enrollment.Mint(ctx, target, inventory, now); err != nil {
			t.Errorf("recovery grant during maintenance boot = %v, want success", err)
		}
	})
	if leaf.Subject.CommonName == "injected" || len(leaf.DNSNames) != 0 || len(leaf.URIs) != 1 ||
		!leaf.NotAfter.Equal(issuer.NotAfter) || leaf.CheckSignatureFrom(issuer) != nil ||
		len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Errorf("issued certificate did not enforce controller identity and issuer validity: %+v", leaf)
	}
	replay, err := enrollment.Enroll(ctx, token, csr, now.Add(time.Minute))
	if err != nil || !bytes.Equal(replay, first) {
		t.Errorf("same-key retry = %v, want exact issued certificate", err)
	}
	if retainedImage != "" {
		if err := enrollment.CleanupDelivery(ctx, "op", ref, mediaRoot); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(retainedImage); err != nil {
			t.Fatalf("cleanup before authenticated contact removed the image: %v", err)
		}
	}
	clock := func() time.Time { return now }
	mux := http.NewServeMux()
	mux.Handle("/internal/v1/guest/poll", web.GuestPollHandler(
		web.GuestCompatibility{Protocol: "1", CurrentRelease: "test", PreviousRelease: "previous"}, enrollment.Exchange, clock, ""))
	const gitPath = "/internal/v1/guest/git/github:access-token"
	resolveGitToken := func(_ context.Context, owner, repository string) (string, error) {
		if owner != "owner" || repository != "https://github.com/example/test" {
			t.Error("token lookup did not use the owner and repository from the guest registration")
		}
		var mode string
		if err := db.QueryRow("SELECT display_name FROM users WHERE id='owner'").Scan(&mode); err != nil {
			return "", err
		}
		switch mode {
		case "revoke":
			_, err := db.Exec("UPDATE guest_registrations SET state='revoked' WHERE workspace_id='work'")
			return "fixture-no-longer-authorized", err
		case "reconnect":
			return "", identity.ErrGitHubReconnect
		default:
			return "fixture-user-access-token", nil
		}
	}
	mux.Handle(gitPath, web.GuestGitTokenHandler(enrollment.Authenticate, resolveGitToken, clock))
	server := httptest.NewUnstartedServer(mux)
	server.TLS = guest.ServerTLS(leafFixture(t, issuerKey, issuer, false), ca)
	server.StartTLS()
	defer server.Close()
	transport := &http.Transport{TLSClientConfig: guest.ClientTLS(
		tls.Certificate{Certificate: [][]byte{first}, PrivateKey: clientKey}, ca, "controller.test")}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	t.Run("controller tunnel listener", func(t *testing.T) {
		registry := &proxy.Registry{}
		tunnels := &proxy.Tunnels{Registry: registry}
		fence := &backup.WriteFence{}
		const bind = "127.0.0.1:0"
		cipher, err := cryptostore.New(make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		secrets := secret.NewService(cipher)
		if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			if _, err := tx.Exec("UPDATE workspaces SET observed_state='running' WHERE id='work'"); err != nil {
				return err
			}
			for _, item := range []struct{ scope, id, value string }{
				{"organization", install, "fallback-fixture"}, {"user", "owner", ""},
			} {
				if err := secrets.Set(ctx, tx, secret.Scope{Type: item.scope, ID: item.id}, "TOKEN", []byte(item.value), "owner", now); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		listeners, err := app.ListenGuests(app.GuestListenerConfig{
			EnrollmentAddress: bind, PollAddress: bind, TunnelAddress: bind, PublicHost: "127.0.0.1", Tunnels: tunnels, Secrets: secrets,
			Checkpoint: fence,
		}, enrollment, ca, web.GuestCompatibility{Protocol: "1", CurrentRelease: "test"}, clock)
		if err != nil {
			t.Fatal(err)
		}
		defer listeners.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- listeners.Serve(ctx) }()
		pollTransport := &http.Transport{TLSClientConfig: guest.ClientTLS(
			tls.Certificate{Certificate: [][]byte{first}, PrivateKey: clientKey}, ca, "127.0.0.1")}
		defer pollTransport.CloseIdleConnections()
		pollClient := &http.Client{Transport: pollTransport, Timeout: time.Second}
		secretURL := strings.TrimSuffix(listeners.PollURL, "/poll") + "/secrets/TOKEN:resolve"
		checkSecret := func(url, payload string, code int, want string) {
			t.Helper()
			reply, err := pollClient.Post(url, "application/json", bytes.NewBufferString(payload))
			if err != nil {
				t.Fatal(err)
			}
			defer reply.Body.Close()
			raw, err := io.ReadAll(reply.Body)
			if err != nil || reply.StatusCode != code || !bytes.Contains(raw, []byte(want)) {
				t.Fatalf("guest secret status = %d, %v, want %d and expected response", reply.StatusCode, err, code)
			}
			if code == 200 && reply.Header.Get("Cache-Control") != "no-store" {
				t.Error("guest secret response was cacheable")
			}
		}
		checkSecret(secretURL, `{"scopeId":"another"}`, 400, "request.invalid_body")
		checkSecret(secretURL, "{}", 200, `"value":"","source":"user"`)
		checkSecret(strings.Replace(secretURL, "TOKEN", "MISSING", 1), "{}", 404, "secret.not_found")
		if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			return secrets.Delete(ctx, tx, secret.Scope{Type: "user", ID: "owner"}, "TOKEN", "owner", now)
		}); err != nil {
			t.Fatal(err)
		}
		checkSecret(secretURL, "", 200, `"value":"fallback-fixture","source":"organization"`)
		// A management boot can be running at the provider while the
		// workspace intent is stopped. Poll access must not expose workloads.
		if _, err := db.Exec(`UPDATE workspaces SET desired_state='stopped',preview_ports_json='[3000]' WHERE id='work'`); err != nil {
			t.Fatal(err)
		}
		checkSecret(secretURL, "{}", 403, "guest.identity_denied")
		managementPoll, err := pollClient.Post(listeners.PollURL, "application/json",
			bytes.NewBufferString(`{"protocolVersion":"1","guestRelease":"test"}`))
		if err != nil {
			t.Fatal(err)
		}
		var maintenanceState struct {
			DesiredState string
			Operation    *guest.PollOperation
		}
		decodeErr := json.NewDecoder(managementPoll.Body).Decode(&maintenanceState)
		managementPoll.Body.Close()
		if managementPoll.StatusCode != 200 || decodeErr != nil || maintenanceState.DesiredState != "stopped" ||
			maintenanceState.Operation != nil || managementPoll.Header.Get(guest.TunnelAddressHeader) != "" ||
			managementPoll.Header.Get(guest.PreviewPortsHeader) != "" {
			t.Fatalf("management-only poll = %d, %+v (%v), want stopped, no workload command, tunnel, or previews",
				managementPoll.StatusCode, maintenanceState, decodeErr)
		}
		if _, err := db.Exec("UPDATE workspaces SET desired_state='running',preview_ports_json='[]' WHERE id='work'"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("UPDATE workspaces SET observed_state='stopped' WHERE id='work'"); err != nil {
			t.Fatal(err)
		}
		checkSecret(secretURL, "{}", 403, "guest.identity_denied")
		if _, err := db.Exec("UPDATE guest_registrations SET state='revoked' WHERE workspace_id='work'"); err != nil {
			t.Fatal(err)
		}
		checkSecret(secretURL, "{}", 403, "guest.identity_denied")
		if _, err := db.Exec("UPDATE guest_registrations SET state='active' WHERE workspace_id='work'"); err != nil {
			t.Fatal(err)
		}
		reply, err := pollClient.Post(listeners.PollURL, "application/json",
			bytes.NewBufferString(`{"protocolVersion":"1","guestRelease":"test"}`))
		if err != nil {
			t.Fatal(err)
		}
		reply.Body.Close()
		tunnelAddress := reply.Header.Get(guest.TunnelAddressHeader)
		if reply.StatusCode != 200 || tunnelAddress != listeners.TunnelAddress {
			t.Fatalf("poll returned %d and tunnel address %q, want 200 and %q", reply.StatusCode, tunnelAddress, listeners.TunnelAddress)
		}
		advance := make(chan struct{})
		workload := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, line := range []string{"started\n", "checkpoint\n", "resumed\n"} {
				select {
				case <-r.Context().Done():
					return
				case <-advance:
				}
				if _, err := io.WriteString(w, line); err != nil {
					return
				}
				if err := http.NewResponseController(w).Flush(); err != nil {
					return
				}
			}
		}))
		defer workload.Close()
		workloadURL, err := url.Parse(workload.URL)
		if err != nil {
			t.Fatal(err)
		}
		guestDone := make(chan error, 1)
		go func() {
			cfg := guest.ClientTLS(tls.Certificate{Certificate: [][]byte{first}, PrivateKey: clientKey}, ca, "127.0.0.1")
			guestDone <- guest.ServeTunnel(ctx, tunnelAddress, cfg, "1", 1,
				[]proxy.TunnelService{{Key: "ide", Target: workloadURL.Host, Protocols: []string{"http"}}})
		}()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			stream, err := tunnels.Open(ctx, "work", 1, proxy.StreamHeader{ServiceKey: "ide", RequestID: "fixture", Protocol: "http"})
			if err == nil {
				// A backup fences new controller writes. An established guest
				// workload must keep making progress throughout that interval.
				t.Run("workload continues during backup checkpoint", func(t *testing.T) {
					defer stream.Close()
					deadline, _ := ctx.Deadline()
					if err := stream.SetDeadline(deadline); err != nil {
						t.Fatal(err)
					}
					request := httptest.NewRequestWithContext(ctx, http.MethodGet, workload.URL, nil)
					if err := request.Write(stream); err != nil {
						t.Fatal(err)
					}
					step := func() {
						t.Helper()
						select {
						case advance <- struct{}{}:
						case <-ctx.Done():
							t.Fatal("workload stopped before acknowledging the next step")
						}
					}
					step()
					response, err := http.ReadResponse(bufio.NewReader(stream), request)
					if err != nil {
						t.Fatal(err)
					}
					body := bufio.NewReader(response.Body)
					expectLine := func(want string) {
						t.Helper()
						if got, err := body.ReadString('\n'); err != nil || got != want {
							t.Fatalf("workload output = %q, %v, want %q", got, err, want)
						}
					}
					expectLine("started\n")
					resume, err := fence.Pause(ctx)
					if err != nil {
						t.Fatal(err)
					}
					defer resume()
					pollCtx, stopPoll := context.WithTimeout(ctx, 50*time.Millisecond)
					defer stopPoll()
					poll, err := http.NewRequestWithContext(pollCtx, http.MethodPost, listeners.PollURL,
						strings.NewReader(`{"protocolVersion":"1","guestRelease":"test"}`))
					if err != nil {
						t.Fatal(err)
					}
					poll.Header.Set("Content-Type", "application/json")
					if blocked, err := pollClient.Do(poll); blocked != nil {
						blocked.Body.Close()
						t.Fatalf("guest poll during checkpoint returned %d, want it to wait for the fence", blocked.StatusCode)
					} else if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("guest poll during checkpoint = %v, want its deadline to expire", err)
					}
					step()
					expectLine("checkpoint\n")
					resume()
					step()
					expectLine("resumed\n")
					if err := response.Body.Close(); err != nil {
						t.Fatal(err)
					}
				})
				break
			}
			select {
			case err := <-guestDone:
				t.Fatalf("enrolled guest could not establish a tunnel: %v", err)
			case <-ctx.Done():
				t.Fatal("enrolled guest tunnel never became available")
			case <-ticker.C:
			}
		}
		listeners.Close()
		select {
		case err := <-done:
			if err == nil {
				t.Error("closed listeners returned no shutdown result")
			}
		case <-ctx.Done():
			t.Fatal("controller did not stop its tunnel listener")
		}
		select {
		case <-guestDone:
		case <-ctx.Done():
			t.Fatal("guest tunnel did not close with its controller listener")
		}
	})
	pollResponse := func(payload string) (int, string) {
		t.Helper()
		response, err := client.Post(server.URL+"/internal/v1/guest/poll", "application/json", bytes.NewBufferString(payload))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var body bytes.Buffer
		if _, err := body.ReadFrom(response.Body); err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, body.String()
	}
	poll := func(payload string) int { code, _ := pollResponse(payload); return code }
	const hello = `{"protocolVersion":"1","guestRelease":"test"}`
	if code := poll(hello); code != 200 {
		t.Fatalf("registered poll = %d, want 200", code)
	}
	// Probe snapshots are scoped to the authenticated generation and expire
	// even if a guest keeps polling without reporting another check.
	t.Run("authenticated probe reports", func(t *testing.T) {
		report := func(probes string) int {
			return poll(strings.TrimSuffix(hello, "}") + `,"probes":` + probes + "}")
		}
		check := func(generation int64, stamp time.Time, want string) {
			t.Helper()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			probes, err := workspace.ReadProbeResults(ctx, tx, "work", generation, stamp)
			if err != nil || (want == "" && len(probes) != 0) ||
				(want != "" && (len(probes) != 1 || probes[0].ID != workspace.ProbeCodeServer || probes[0].Status != want)) {
				t.Fatalf("probe results = %+v (%v), want code-server status %q", probes, err, want)
			}
		}
		check(1, now, "")
		if code := report(`[{"id":"code-server","status":"healthy"}]`); code != 200 {
			t.Fatalf("valid probe poll = %d, want 200", code)
		}
		check(1, now, workspace.ProbeHealthy)
		check(2, now, "")
		if code := report(`[{"id":"code-server","status":"error"},{"id":"code-server","status":"error"}]`); code != 400 {
			t.Fatalf("duplicate probe poll = %d, want 400", code)
		}
		check(1, now, workspace.ProbeHealthy)
		if code := report(`[{"id":"code-server","status":"error"}]`); code != 200 {
			t.Fatalf("failing probe poll = %d, want 200", code)
		}
		if code := poll(hello); code != 200 {
			t.Fatalf("poll without probes = %d, want 200", code)
		}
		check(1, now, workspace.ProbeError)
		check(1, now.Add(91*time.Second), "")
	})
	t.Run("guest Git credential authority", func(t *testing.T) {
		// Switch provider behavior through the fixture database so the request
		// and test goroutines need no shared mutable state.
		mode := func(value string) {
			t.Helper()
			if _, err := db.Exec("UPDATE users SET display_name=$1 WHERE id='owner'", value); err != nil {
				t.Fatal(err)
			}
		}
		request := func(payload string, want int) string {
			t.Helper()
			response, err := client.Post(server.URL+gitPath, "application/json", bytes.NewBufferString(payload))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var body bytes.Buffer
			if _, err := body.ReadFrom(response.Body); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != want || response.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("guest Git token request = %d, cache %q, want %d and no-store", response.StatusCode, response.Header.Get("Cache-Control"), want)
			}
			return body.String()
		}
		mode("connected")
		if _, err := db.Exec("UPDATE workspaces SET desired_state='stopped',observed_state='running' WHERE id='work'"); err != nil {
			t.Fatal(err)
		}
		if body := request("", 403); strings.Contains(body, "fixture-user-access-token") {
			t.Error("management-only boot disclosed a Git access token")
		}
		if _, err := db.Exec("UPDATE workspaces SET desired_state='running',observed_state='stopped' WHERE id='work'"); err != nil {
			t.Fatal(err)
		}
		request(`{"ownerId":"other","repositoryUrl":"https://github.com/example/other"}`, 400)
		req, err := http.NewRequest(http.MethodPost, server.URL+gitPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-ShiftBox-Git-Repository", "https://github.com/another/repo")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatalf("token with mismatched repository context = %d, want 403", response.StatusCode)
		}
		body := request("", 200)
		var got map[string]string
		if err := json.Unmarshal([]byte(body), &got); err != nil || len(got) != 1 || got["accessToken"] != "fixture-user-access-token" {
			t.Error("guest Git token response was not limited to the current access token")
		}
		mode("reconnect")
		if body := request("", 409); !bytes.Contains([]byte(body), []byte("git.reconnect_required")) {
			t.Error("guest Git token lookup did not communicate the reconnect requirement")
		}
		mode("revoke")
		if body := request("", 403); bytes.Contains([]byte(body), []byte("fixture-no-longer-authorized")) {
			t.Error("guest received Git token after revocation during provider lookup")
		}
		if _, err := db.Exec("UPDATE guest_registrations SET state='active' WHERE workspace_id='work'"); err != nil {
			t.Fatal(err)
		}
	})
	if code := poll(`{"protocolVersion":"1","guestRelease":"test","workspaceId":"foreign"}`); code != 400 {
		t.Errorf("poll with forged workspace = %d, want 400", code)
	}
	// The previous release may still poll, but unsupported releases do not.
	if code := poll(`{"protocolVersion":"1","guestRelease":"previous"}`); code != 200 {
		t.Errorf("previous-release poll = %d, want 200", code)
	}
	if code := poll(`{"protocolVersion":"1","guestRelease":"unsupported"}`); code != 409 {
		t.Errorf("unsupported-release poll = %d, want 409", code)
	}
	// The stored compatibility evidence follows accepted polls even after a
	// downgrade. A rejected version cannot replace the last accepted report.
	var lastRelease, lastProtocol sql.NullString
	if err := db.QueryRow(`SELECT guest_release,protocol_version FROM workspaces WHERE id='work'`).Scan(&lastRelease, &lastProtocol); err != nil ||
		lastRelease.String != "previous" || lastProtocol.String != "1" {
		t.Errorf("last accepted guest poll = %q/%q, %v, want previous/1", lastRelease.String, lastProtocol.String, err)
	}
	// A stopped guest can still block the next upgrade after rolling back.
	blockers, err = store.GuestUpgradeBlockers(ctx, install, "test", "1")
	if err != nil || len(blockers) != 1 || blockers[0] != (release.GuestUpgradeBlocker{
		WorkspaceID: "work", ObservedState: "stopped", GuestRelease: "previous", ProtocolVersion: "1",
	}) {
		t.Errorf("guest upgrade blockers after rollback = %+v, %v, want stopped previous/1 guest", blockers, err)
	}
	if code := poll(hello); code != 200 {
		t.Fatalf("current-release poll = %d, want 200", code)
	}
	if blockers, err := store.GuestUpgradeBlockers(ctx, install, "test", "1"); err != nil || len(blockers) != 0 {
		t.Errorf("guest upgrade blockers after current poll = %+v, %v, want none", blockers, err)
	}
	const configureID = "99999999-0000-4000-8000-000000000001"
	if _, err := db.Exec(`INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,current_step,created_at,version,originating_action)
		VALUES($1,'work','provision',1,'owner','fixture-configuration','waiting','configure','now',7,'workspace.launch');
	`, configureID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO operation_steps(operation_id,step_index,step_key,label,status)
		VALUES($1,0,'configure','Configure','active')`, configureID); err != nil {
		t.Fatal(err)
	}
	// Enrolled guests poll before the provider worker confirms that the VM is running.
	if code, body := pollResponse(hello); code != 200 || !bytes.Contains([]byte(body), []byte(`"operation":null`)) {
		t.Fatalf("stopped workspace poll = %d %s, want no configuration dispatch", code, body)
	}
	if _, err := db.Exec(`UPDATE workspaces SET observed_state='running' WHERE id='work'`); err != nil {
		t.Fatal(err)
	}
	var dispatch struct {
		Operation struct {
			ID        string
			Version   int64
			Command   string
			Arguments struct{ DevcontainerPath string }
		}
	}
	code, body := pollResponse(hello)
	if code != 200 || json.Unmarshal([]byte(body), &dispatch) != nil || dispatch.Operation.ID != configureID ||
		dispatch.Operation.Version != 7 || dispatch.Operation.Command != "ensure-development-container" ||
		dispatch.Operation.Arguments.DevcontainerPath != "devcontainer.json" {
		t.Fatalf("configure poll = %d %s, want version-7 dispatch for the enrolled workspace snapshot", code, body)
	}
	if _, err := db.Exec(`UPDATE operations SET version=version+1 WHERE id=$1`, configureID); err != nil {
		t.Fatal(err)
	}
	// Worker leases can advance the operation version while an already issued
	// dispatch remains in flight.
	if code, replay := pollResponse(hello); code != 200 || replay != body {
		t.Fatalf("lost-dispatch replay = %d %s, want the original response %s", code, replay, body)
	}
	const containerID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	makeReport := func(version, generation, status, step, container string) string {
		return `{"protocolVersion":"1","guestRelease":"test","lastOperationId":"` + configureID + `","lastOperationVersion":` + version +
			`,"operationReport":{"operationId":"` + configureID + `","workspaceGeneration":` + generation + `,"status":"` + status +
			`","step":"` + step + `","attempt":1,"evidence":{"containerId":"` + container + `","waitForReached":true}}}`
	}
	if code := poll(makeReport("6", "1", "running", "devcontainer.wait_for", containerID)); code != 409 {
		t.Errorf("stale version poll = %d, want 409", code)
	}
	if code := poll(makeReport("7", "2", "running", "devcontainer.wait_for", containerID)); code != 409 {
		t.Errorf("forged generation poll = %d, want 409", code)
	}
	if code := poll(makeReport("7", "1", "running", "devcontainer.wait_for", "unbounded evidence")); code != 409 {
		t.Errorf("invalid container ID poll = %d, want 409", code)
	}
	if code := poll(makeReport("7", "1", "succeeded", "devcontainer.lifecycle", containerID)); code != 409 {
		t.Errorf("completion before waitFor poll = %d, want 409", code)
	}
	const otherID = "99999999-0000-4000-8000-000000000002"
	if _, err := db.Exec(`INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,current_step,created_at,version)
		VALUES($1,'other-workspace','provision',1,'owner','other-configuration','waiting','configure','now',7)`, otherID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO guest_dispatches(operation_id,dispatch_version,status) VALUES($1,7,'pending')`, otherID); err != nil {
		t.Fatal(err)
	}
	runningReport := makeReport("7", "1", "running", "devcontainer.wait_for", containerID)
	if code := poll(string(bytes.ReplaceAll([]byte(runningReport), []byte(configureID), []byte(otherID)))); code != 409 {
		t.Errorf("cross-workspace report = %d, want 409", code)
	}
	for range 2 {
		if code := poll(runningReport); code != 200 {
			t.Fatalf("waitFor report or replay = %d, want 200", code)
		}
	}
	var status, storedContainer string
	if err := db.QueryRow(`SELECT status,container_id FROM guest_dispatches WHERE operation_id=$1`, configureID).Scan(&status, &storedContainer); err != nil ||
		status != "running" || storedContainer != containerID {
		t.Fatalf("persisted waitFor report = (%q, %q, %v), want running with container evidence", status, storedContainer, err)
	}
	done := makeReport("7", "1", "succeeded", "devcontainer.lifecycle", containerID)
	if _, err := db.Exec(`UPDATE operation_steps SET status='succeeded' WHERE operation_id=$1 AND step_key='configure'`, configureID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE operations SET current_step='verify_ide' WHERE id=$1`, configureID); err != nil {
		t.Fatal(err)
	}
	if code, response := pollResponse(runningReport); code != 200 || !bytes.Contains([]byte(response), []byte(`"command":"ensure-development-container"`)) {
		t.Fatalf("poll while verifying IDE = %d %s, want the existing dispatch for later hooks", code, response)
	}
	for range 2 {
		if code, body := pollResponse(done); code != 200 || !bytes.Contains([]byte(body), []byte(`"operation":null`)) {
			t.Fatalf("terminal report or replay = %d %s, want no new dispatch", code, body)
		}
	}
	if code := poll(runningReport); code != 409 {
		t.Errorf("regressing terminal report = %d, want 409", code)
	}
	// Rebuild dispatches through the same authenticated, versioned exchange,
	// but only with a matching running reservation and rebuild intent.
	t.Run("guest container rebuild", func(t *testing.T) {
		const rebuildID = "99999999-0000-4000-8000-000000000014"
		for _, query := range []string{
			`INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,current_step,created_at,version,originating_action)
			 VALUES($1,'work','rebuild',1,'owner','fixture-rebuild','waiting','configure','now',7,'workspace.rebuild')`,
			`INSERT INTO operation_steps(operation_id,step_index,step_key,label,status) VALUES($1,0,'configure','Rebuild','active')`,
		} {
			if _, err := db.Exec(query, rebuildID); err != nil {
				t.Fatal(err)
			}
		}
		if code, body := pollResponse(hello); code != 200 || !strings.Contains(body, `"operation":null`) {
			t.Fatalf("unreserved rebuild poll = %d %s, want no dispatch", code, body)
		}
		if _, err := db.Exec(`INSERT INTO reservations(workspace_id,cpu_count,memory_mib,disk_mib,cpu_memory_state,disk_state,reserved_at)
			VALUES('work',2,1024,8192,'reserved','reserved','now')`); err != nil {
			t.Fatal(err)
		}
		code, body := pollResponse(hello)
		if code != 200 || json.Unmarshal([]byte(body), &dispatch) != nil || dispatch.Operation.ID != rebuildID ||
			dispatch.Operation.Command != "rebuild-development-container" || dispatch.Operation.Version != 7 {
			t.Fatalf("rebuild poll = %d %s, want version-7 rebuild dispatch", code, body)
		}
		const replacement = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		report := strings.ReplaceAll(makeReport("7", "1", "running", "devcontainer.wait_for", replacement), configureID, rebuildID)
		if code := poll(report); code != 200 {
			t.Fatalf("rebuild waitFor poll = %d, want 200", code)
		}
		if err := db.QueryRow("SELECT status,container_id FROM guest_dispatches WHERE operation_id=$1", rebuildID).Scan(&status, &storedContainer); err != nil ||
			status != "running" || storedContainer != replacement {
			t.Fatalf("rebuild evidence = %q/%q, %v, want running with replacement identity", status, storedContainer, err)
		}
		if _, err := db.Exec("UPDATE operations SET status='succeeded' WHERE id=$1", rebuildID); err != nil {
			t.Fatal(err)
		}
	})
	// Repair uses its own command and report, with no repository arguments
	// and no assertion that the development container reached waitFor.
	t.Run("guest repair entry", func(t *testing.T) {
		const repairID = "99999999-0000-4000-8000-000000000015"
		for _, query := range []string{
			`INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,current_step,created_at,version,originating_action)
			 VALUES($1,'work','repair',1,'owner','fixture-repair','waiting','repair_enter','now',9,'workspace.repair')`,
			`INSERT INTO operation_steps(operation_id,step_index,step_key,label,status) VALUES($1,0,'repair_enter','Repair','active')`,
		} {
			if _, err := db.Exec(query, repairID); err != nil {
				t.Fatal(err)
			}
		}
		code, body := pollResponse(hello)
		var repair struct{ Operation guest.PollOperation }
		if code != 200 || json.Unmarshal([]byte(body), &repair) != nil || repair.Operation.ID != repairID ||
			repair.Operation.Command != "enter-repair" || repair.Operation.Arguments.RepositoryURL != "" ||
			repair.Operation.Arguments.RepositoryRef != "" || repair.Operation.Arguments.Devcontainer != "" {
			t.Fatalf("repair poll = %d %s, want enter-repair without project arguments", code, body)
		}
		report := `{"protocolVersion":"1","guestRelease":"test","lastOperationId":"` + repairID + `","lastOperationVersion":9,
			"operationReport":{"operationId":"` + repairID + `","workspaceGeneration":1,"status":"succeeded",
			"step":"repair.enter","attempt":1,"evidence":{"containerId":"` + containerID + `","waitForReached":false}}}`
		if code := poll(strings.Replace(report, "repair.enter", "devcontainer.lifecycle", 1)); code != 409 {
			t.Fatalf("wrong repair report step = %d, want 409", code)
		}
		for range 2 {
			if code, body := pollResponse(report); code != 200 || !strings.Contains(body, `"operation":null`) {
				t.Fatalf("repair report or replay = %d %s, want accepted with no new dispatch", code, body)
			}
		}
		if _, err := db.Exec("UPDATE operations SET status='succeeded' WHERE id=$1", repairID); err != nil {
			t.Fatal(err)
		}
		const retryID = "99999999-0000-4000-8000-000000000016"
		for _, query := range []string{
			`INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,current_step,created_at,version,originating_action)
			 VALUES($1,'work','rebuild',1,'owner','fixture-repair-retry','waiting','configure','now',7,'workspace.repair')`,
			`INSERT INTO operation_steps(operation_id,step_index,step_key,label,status) VALUES($1,0,'configure','Retry setup','active')`,
		} {
			if _, err := db.Exec(query, retryID); err != nil {
				t.Fatal(err)
			}
		}
		code, body = pollResponse(report)
		if code != 200 || json.Unmarshal([]byte(body), &repair) != nil || repair.Operation.ID != retryID ||
			repair.Operation.Command != "retry-development-after-repair" || repair.Operation.Arguments.Devcontainer != "devcontainer.json" {
			t.Fatalf("repair retry poll = %d %s, want fixed command and captured project configuration", code, body)
		}
		retried := strings.ReplaceAll(makeReport("7", "1", "running", "devcontainer.wait_for", containerID), configureID, retryID)
		if code := poll(retried); code != 200 {
			t.Fatalf("retried setup waitFor report = %d, want 200", code)
		}
		if _, err := db.Exec("UPDATE operations SET status='succeeded' WHERE id=$1", retryID); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("guest workload shutdown", func(t *testing.T) {
		const stopID = "99999999-0000-4000-8000-000000000003"
		if _, err := db.Exec("UPDATE workspaces SET desired_state='stopped' WHERE id='work'"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,current_step,created_at,version,originating_action)
			VALUES($1,'work','stop',1,'owner','fixture-shutdown','waiting','shutdown','now',9,'workspace.stop')`, stopID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO operation_steps(operation_id,step_index,step_key,label,status)
			VALUES($1,0,'shutdown','Shutdown','active')`, stopID); err != nil {
			t.Fatal(err)
		}
		code, body := pollResponse(done)
		if code != 200 || !bytes.Contains([]byte(body), []byte(`"command":"shutdown-for-stop"`)) {
			t.Fatalf("poll after accepted stop = %d %s, want shutdown dispatch", code, body)
		}
		const report = `{"protocolVersion":"1","guestRelease":"test","lastOperationId":"` + stopID + `","lastOperationVersion":9,
			"operationReport":{"operationId":"` + stopID + `","workspaceGeneration":1,
			"status":"succeeded","step":"workload.shutdown","attempt":1,"evidence":{"waitForReached":false}}}`
		for range 2 {
			if code, body := pollResponse(report); code != 200 || !bytes.Contains([]byte(body), []byte(`"operation":null`)) {
				t.Fatalf("workload shutdown report or replay = %d %s, want accepted and no dispatch", code, body)
			}
		}
		if err := db.QueryRow("SELECT status FROM guest_dispatches WHERE operation_id=$1", stopID).Scan(&status); err != nil || status != "succeeded" {
			t.Fatalf("shutdown status = %q, %v, want persisted success", status, err)
		}
	})
	t.Run("guest configuration after restart", func(t *testing.T) {
		const startID = "99999999-0000-4000-8000-000000000004"
		if _, err := db.Exec("UPDATE workspaces SET desired_state='running' WHERE id='work'"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,current_step,created_at,version,originating_action)
			VALUES($1,'work','start',1,'owner','fixture-restart','waiting','configure','now',11,'workspace.start')`, startID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO operation_steps(operation_id,step_index,step_key,label,status)
			VALUES($1,0,'configure','Configure','active')`, startID); err != nil {
			t.Fatal(err)
		}
		code, body := pollResponse(hello)
		if code != 200 || !bytes.Contains([]byte(body), []byte(`"id":"`+startID+`"`)) ||
			!bytes.Contains([]byte(body), []byte(`"command":"ensure-development-container"`)) {
			t.Fatalf("restart poll = %d %s, want the start operation's configuration dispatch", code, body)
		}
		startReport := func(report string) string {
			replaced := bytes.ReplaceAll([]byte(report), []byte(configureID), []byte(startID))
			return string(bytes.ReplaceAll(replaced, []byte(`"lastOperationVersion":7`), []byte(`"lastOperationVersion":11`)))
		}
		if code := poll(startReport(runningReport)); code != 200 {
			t.Fatalf("restart waitFor report = %d, want 200", code)
		}
		if code := poll(startReport(done)); code != 200 {
			t.Fatalf("restart setup completion = %d, want 200", code)
		}
	})
	// A committed deletion permits only the current graceful shutdown poll.
	t.Run("committed deletion permits shutdown only", func(t *testing.T) {
		const deleteID = "99999999-0000-4000-8000-000000000005"
		if _, err := db.Exec("UPDATE workspaces SET desired_state='deleted',deletion_commit_at=$1 WHERE id='work'", now.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,current_step,created_at,version,originating_action)
			VALUES($1,'work','delete',1,'owner','explicit-delete-fixture','waiting','shutdown','now',13,'workspace.delete')`, deleteID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO operation_steps(operation_id,step_index,step_key,label,status)
			VALUES($1,0,'shutdown','Shutdown','active')`, deleteID); err != nil {
			t.Fatal(err)
		}
		code, body := pollResponse(hello)
		if code != 200 || !strings.Contains(body, `"command":"shutdown-for-stop"`) ||
			!strings.Contains(body, `"desiredState":"stopped"`) || !strings.Contains(body, `"type":"stop"`) {
			t.Fatalf("delete poll = %d %s, want compatible shutdown command", code, body)
		}
		const report = `{"protocolVersion":"1","guestRelease":"test","lastOperationId":"` + deleteID + `","lastOperationVersion":13,
			"operationReport":{"operationId":"` + deleteID + `","workspaceGeneration":1,
			"status":"succeeded","step":"workload.shutdown","attempt":1,"evidence":{"waitForReached":false}}}`
		if code := poll(report); code != 200 {
			t.Fatalf("delete shutdown report = %d, want 200", code)
		}
		if _, err := db.Exec("UPDATE operations SET current_step='stop' WHERE id=$1", deleteID); err != nil {
			t.Fatal(err)
		}
		if code := poll(hello); code != 403 {
			t.Fatalf("poll after delete shutdown = %d, want 403", code)
		}
	})
	var authenticated string
	if err := db.QueryRow("SELECT last_authenticated_at FROM guest_registrations WHERE workspace_id='work'").Scan(&authenticated); err != nil ||
		authenticated != now.Format("2006-01-02T15:04:05.000000000Z") {
		t.Errorf("authenticated contact = %q, %v, want controller time", authenticated, err)
	}
	if retainedImage != "" {
		reopened := guest.NewEnrollment(db, ca)
		for range 2 {
			if err := reopened.CleanupDelivery(ctx, "op", ref, mediaRoot); err != nil {
				t.Fatalf("cleanup confirmed enrollment: %v", err)
			}
		}
		blank, err := os.ReadFile(retainedImage)
		if err != nil || len(blank) == 0 || bytes.Contains(blank, []byte(token)) || bytes.Contains(blank, []byte("ENROLLMENT.JSON")) {
			t.Fatalf("media after cleanup must remain bootable as blank media without the seed: %v", err)
		}
	}
	if _, err := enrollment.Enroll(ctx, token, fixtureCSR(t), now); !errors.Is(err, guest.ErrEnrollmentDenied) {
		t.Errorf("alternate key = %v, want denial", err)
	}
	if _, err := enrollment.Enroll(ctx, token, csr, now.Add(15*time.Minute)); !errors.Is(err, guest.ErrEnrollmentDenied) {
		t.Errorf("expired grant = %v, want denial", err)
	}
	if _, err := db.Exec("UPDATE guest_registrations SET state='revoked' WHERE workspace_id='work'"); err != nil {
		t.Fatal(err)
	}
	if code := poll(hello); code != 403 {
		t.Errorf("poll after revocation = %d, want 403", code)
	}
	if _, err := enrollment.Enroll(ctx, token, csr, now); !errors.Is(err, guest.ErrEnrollmentDenied) {
		t.Errorf("revoked registration replay = %v, want denial", err)
	}
}

func enrollOverTLS(t *testing.T, ca *guest.Authority, serverLeaf tls.Certificate, enrollment *guest.Enrollment, token string, csr []byte, now time.Time) []byte {
	t.Helper()
	handler := web.GuestEnrollmentHandler(web.GuestCompatibility{Protocol: "1", CurrentRelease: "test"},
		enrollment.Enroll, func() time.Time { return now })
	server := httptest.NewUnstartedServer(handler)
	server.TLS = guest.EnrollmentTLS(serverLeaf)
	server.StartTLS()
	t.Cleanup(server.Close)
	client := server.Client()
	transport := client.Transport.(*http.Transport)
	noClientCert := guest.ClientTLS(tls.Certificate{}, ca, "controller.test")
	noClientCert.Certificates = nil
	transport.TLSClientConfig = noClientCert
	serverCertRequest := map[string]string{"protocolVersion": "1", "guestRelease": "test", "grant": token,
		"csrPem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}))}
	body, err := json.Marshal(serverCertRequest)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Post(server.URL+"/internal/v1/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("real enrollment returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		CertificatePEM string `json:"certificatePem"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	cert, _ := pem.Decode([]byte(result.CertificatePEM))
	if cert == nil || cert.Type != "CERTIFICATE" {
		t.Fatal("enrollment response omitted the certificate")
	}
	return cert.Bytes
}

func fixtureCSR(t *testing.T) []byte {
	t.Helper()
	csr, _ := fixtureCSRWithKey(t)
	return csr
}

func fixtureCSRWithKey(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "injected"}, DNSNames: []string{"injected.test"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	return csr, key
}
