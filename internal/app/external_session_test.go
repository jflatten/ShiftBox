package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"golang.org/x/oauth2"
)

// TestExternalMembershipAdmission verifies first sign-in requests approval,
// equal emails never merge accounts, and a disabled binding stays denied.
func TestExternalMembershipAdmission(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	db, installation, err := database.OpenBootstrap(ctx, filepath.Join(t.TempDir(), "external.db"), database.BootstrapSeed{
		ID: uuid.NewString(), Organization: "ShiftBox", Origin: "https://management.test", IDEPattern: "{workspace}.ide.test",
		PreviewPattern: "{service}--{workspace}.preview.test", Release: "test", MinGuestRelease: "test", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := auth.NewStore(db)
	localID := uuid.NewString()
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := store.CreateUser(ctx, tx, auth.User{ID: localID, PrimaryEmail: "same@example.test",
			Role: "administrator", MembershipState: "active", CredentialMode: "local"}, "fixture", now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE installations SET state='active' WHERE id=$1", installation)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	subject := identity.VerifiedIdentity{Issuer: "https://upstream.test", Subject: "stable",
		ConnectorID: "company", Email: "same@example.test", EmailVerified: true, AuthorizedAt: now, DexConnectorID: "broker"}
	if _, err := db.ExecContext(ctx, "INSERT INTO external_connector(installation_id,issuer,connector_id,dex_id) VALUES($1,$2,$3,$4)",
		installation, subject.Issuer, subject.ConnectorID, subject.DexConnectorID); err != nil {
		t.Fatal(err)
	}
	issue := func() (auth.Session, string, error) {
		t.Helper()
		return issueExternalSession(ctx, db, installation, subject, now, uuid.NewString)
	}
	if _, token, err := issue(); !errors.Is(err, errMembershipPending) || token != "" {
		t.Fatalf("first login yielded a token or %v, want pending approval", err)
	}
	var userID string
	var role, state string
	if err := db.QueryRowContext(ctx, `SELECT u.id,u.role,u.membership_state FROM users u
		JOIN identity_bindings b ON u.id=b.user_id WHERE b.issuer=$1 AND b.subject=$2 AND b.connector_id=$3`,
		subject.Issuer, subject.Subject, subject.ConnectorID).Scan(&userID, &role, &state); err != nil ||
		userID == localID || role != "member" || state != "pending" {
		t.Fatalf("first external login resolved role=%q state=%q and error=%v, want a separate pending member", role, state, err)
	}
	if _, token, err := issue(); !errors.Is(err, errMembershipPending) || token != "" {
		t.Fatalf("pending re-login yielded a token or %v, want pending approval", err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE users SET membership_state='active' WHERE id=$1", userID); err != nil {
		t.Fatal(err)
	}
	subject.Email = "changed@example.test"
	session, token, err := issue()
	if err != nil || token == "" || session.UserID != userID || session.ExternalAuthorizedAt == nil || !session.ExternalAuthorizedAt.Equal(now) {
		t.Fatalf("active external session did not preserve the bound user and fresh authorization: %v", err)
	}
	f := externalLogin{db: db, login: &SetupLogin{Config: oauth2.Config{ClientID: "fixture", Endpoint: oauth2.Endpoint{AuthURL: "https://management.test/dex/auth"}}},
		now: func() time.Time { return now }, connector: func(context.Context) (identity.ConnectorIdentity, error) {
			return identity.ConnectorIdentity{ID: subject.ConnectorID, Issuer: subject.Issuer}, nil
		}, stateKey: [32]byte{1}}
	handler := f.reauthorizationHandler("https://management.test")
	req := httptest.NewRequest(http.MethodPost, "https://management.test/api/v1/session/external-reauthorization", nil)
	req.Header.Set("Origin", "https://management.test")
	req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != 403 || len(response.Result().Cookies()) != 0 {
		t.Fatalf("external reauthorization without CSRF = %d, want 403 and no attempt", response.Code)
	}
	req.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != 200 || len(response.Result().Cookies()) != 1 {
		t.Fatalf("external reauthorization with CSRF = %d, want 200 and one attempt", response.Code)
	}
	if _, err := db.ExecContext(ctx, "UPDATE external_connector SET dex_id='replacement' WHERE installation_id=$1", installation); err != nil {
		t.Fatal(err)
	}
	if _, token, err := issue(); !errors.Is(err, identity.ErrInvalidIdentity) || token != "" {
		t.Fatal("a callback for the retired Dex connector issued a session")
	}
	if _, err := db.ExecContext(ctx, "UPDATE external_connector SET dex_id=$1 WHERE installation_id=$2", subject.DexConnectorID, installation); err != nil {
		t.Fatal(err)
	}
	// Renewal of a session must use the same upstream binding and
	// replace the session before its open connections can be retired.
	registry := &proxy.Registry{}
	closed := false
	if _, err := registry.Register(proxy.Scope{SessionID: session.ID}, func() error { return nil }, func() error {
		closed = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	next := now.Add(time.Second)
	subject.AuthorizedAt = next
	grantID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO origin_grants(id,token_digest,user_id,session_id,workspace_id,generation,service_key,origin,permission_basis,expires_at)
		VALUES($1,'fixture',$2,$3,$4,1,'ide','https://workspace.ide.test','workspace.connect_owner',$5)`,
		grantID, userID, session.ID, uuid.NewString(), now.Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	wrong := subject
	wrong.Subject = "wrong"
	if _, token, err := reauthorizeExternalSession(ctx, db, registry, session.ID, wrong, next, uuid.NewString); !errors.Is(err, identity.ErrInvalidIdentity) || token != "" || closed {
		t.Fatal("a different upstream identity affected the existing session")
	}
	renewed, newToken, err := reauthorizeExternalSession(ctx, db, registry, session.ID, subject, next, uuid.NewString)
	if err != nil || newToken == "" || newToken == token || renewed.UserID != userID || !closed ||
		renewed.ExternalAuthorizedAt == nil || !renewed.ExternalAuthorizedAt.Equal(next) {
		t.Fatalf("reauthorizeExternalSession did not replace the existing session and close access: %v", err)
	}
	if _, token, err := reauthorizeExternalSession(ctx, db, registry, session.ID, subject, next, uuid.NewString); !errors.Is(err, identity.ErrInvalidIdentity) || token != "" {
		t.Fatal("a replay issued another session")
	}
	var grantRevoked bool
	if err := db.QueryRowContext(ctx, "SELECT revoked_at IS NOT NULL FROM origin_grants WHERE id=$1", grantID).Scan(&grantRevoked); err != nil || !grantRevoked {
		t.Fatalf("browser grant revoked = %t (%v), want true", grantRevoked, err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE users SET membership_state='disabled' WHERE id=$1", userID); err != nil {
		t.Fatal(err)
	}
	if _, token, err := issue(); !errors.Is(err, identity.ErrInvalidIdentity) || token != "" {
		t.Fatalf("disabled external login yielded a token or %v, want denial", err)
	}
}
