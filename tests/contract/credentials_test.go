package contract_test

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/secret"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestGuestCredentialContext verifies that a guest can read only its owner's
// effective secrets and keeps project-secret access until its grant is revoked.
func TestGuestCredentialContext(t *testing.T) {
	const installation = "00000000-0000-4000-8000-000000000001"
	const template = "00000000-0000-4000-8000-000000000003"
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	dir := t.TempDir()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(dir, "credentials.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	seedWorkspaceLaunch(t, db, template, strings.Repeat("A", 43), now)
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	secrets := secret.NewService(cipher)
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,
		platform_artifact_id,repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,
		desired_state,observed_state,generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
		VALUES('workspace',$1,'member',$2,1,'artifact','https://github.com/example/repo','.devcontainer/devcontainer.json',
		2,1024,40,'{}','running','running',1,'marker','fixture-vm','now','now',1)`, installation, template); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO template_grants(template_id,user_id,can_launch,project_secret_warning_ack_at,granted_by,granted_at)
		VALUES($1,'member',1,'now','owner','now');`, template); err != nil {
			return err
		}
		for _, stmt := range []string{
			`INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,created_at,version)
			VALUES('operation','workspace','provision',1,'member','credentials-fixture','running','now',1)`,
			`INSERT INTO guest_registrations(workspace_id,generation,state) VALUES('workspace',1,'eligible')`,
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		for _, fixture := range []struct {
			scope       secret.Scope
			name, value string
		}{
			{secret.Scope{Type: "user", ID: "owner"}, "TOKEN", "author-private"},
			{secret.Scope{Type: "user", ID: "owner"}, "AUTHOR_ONLY", "author-private"},
			{secret.Scope{Type: "user", ID: "member"}, "TOKEN", ""},
			{secret.Scope{Type: "template", ID: template}, "TOKEN", "project-value"},
			{secret.Scope{Type: "organization", ID: installation}, "TOKEN", "organization-value"},
		} {
			if err := secrets.Set(ctx, tx, fixture.scope, fixture.name, []byte(fixture.value), "owner", now); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	authority, err := app.EnsureInstallationCA(dir, installation, cipher, now)
	if err != nil {
		t.Fatal(err)
	}
	enrollment := guest.NewEnrollment(db, authority)
	ref := provider.WorkspaceRef{InstallationID: installation, WorkspaceID: "workspace", Generation: 1, OwnershipMarker: "marker", InstanceID: "fixture-vm"}
	grant, err := enrollment.Mint(ctx, "operation", provider.Inventory{Ref: ref, InstanceID: ref.InstanceID, Power: provider.Stopped, ObservedAt: now}, now)
	if err != nil {
		t.Fatal(err)
	}
	key := guestContractKey(t)
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := enrollment.Enroll(ctx, grant, csr, now)
	if err != nil {
		t.Fatal(err)
	}
	serverLeaf, err := authority.ServerLeaf([]string{"127.0.0.1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(ctx context.Context, leaf *x509.Certificate, stamp time.Time, name string) ([]byte, string, error) {
		return enrollment.ResolveSecret(ctx, leaf, stamp, secrets, name)
	}
	server := httptest.NewUnstartedServer(web.GuestSecretHandler(resolve, func() time.Time { return now }))
	server.TLS = guest.ServerTLS(serverLeaf, authority)
	server.StartTLS()
	t.Cleanup(server.Close)
	transport := &http.Transport{TLSClientConfig: guest.ClientTLS(tls.Certificate{Certificate: [][]byte{leaf}, PrivateKey: key}, authority, "127.0.0.1")}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request := func(path, body string, want int) []byte {
		t.Helper()
		res, err := client.Post(server.URL+guest.SecretResolvePrefix+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != want || res.Header.Get("Cache-Control") != "no-store" || strings.Contains(string(raw), "author-private") {
			t.Errorf("resolve %s returned status %d and cache policy %q, want %d, no-store and no author secret", path, res.StatusCode, res.Header.Get("Cache-Control"), want)
		}
		return raw
	}
	checkToken := func(value, source string) {
		t.Helper()
		result := struct{ Name, Value, Source string }{}
		if err := json.Unmarshal(request("TOKEN:resolve", "", http.StatusOK), &result); err != nil ||
			result.Name != "TOKEN" || result.Value != value || result.Source != source {
			t.Errorf("resolve TOKEN returned source %q, matching value %t and error %v, want %q and matching value", result.Source, result.Value == value, err, source)
		}
	}
	request("AUTHOR_ONLY:resolve", "", http.StatusNotFound)
	request("TOKEN:resolve?ownerId=owner", "", http.StatusBadRequest)
	request("TOKEN:resolve", `{"scope":"user","scopeId":"owner"}`, http.StatusBadRequest)
	checkToken("", "user")
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		return secrets.Delete(ctx, tx, secret.Scope{Type: "user", ID: "member"}, "TOKEN", "member", now)
	}); err != nil {
		t.Fatal(err)
	}
	checkToken("project-value", "template")
	if _, err := db.ExecContext(ctx, "UPDATE project_templates SET state='archived',archived_at='now' WHERE id=$1", template); err != nil {
		t.Fatal(err)
	}
	checkToken("project-value", "template")
	if _, err := db.ExecContext(ctx, "UPDATE template_grants SET revoked_at='now' WHERE template_id=$1 AND user_id='member'", template); err != nil {
		t.Fatal(err)
	}
	checkToken("organization-value", "organization")
}
