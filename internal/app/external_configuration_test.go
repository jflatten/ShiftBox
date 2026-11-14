package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

type dexConnectorFixture struct {
	stageErr error
	staged   string
	deleted  []string
}

func (f *dexConnectorFixture) Create(_ context.Context, id, _ string, _ identity.ExternalConnectorConfig) error {
	if f.stageErr != nil {
		return f.stageErr
	}
	f.staged = id
	return nil
}

func (f *dexConnectorFixture) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

// TestExternalConfiguration verifies versioned activation and ensures a
// failed Dex write leaves the live connector and browser session unchanged.
func TestExternalConfiguration(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC()
	const origin = "https://management.test"
	db, installation, err := database.OpenBootstrap(ctx, filepath.Join(t.TempDir(), "state.db"), database.BootstrapSeed{
		ID: uuid.NewString(), Organization: "ShiftBox", Origin: origin, IDEPattern: "{workspace}.ide.test",
		PreviewPattern: "{service}--{workspace}.preview.test", Release: "test", MinGuestRelease: "test", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := auth.NewStore(db)
	admin := uuid.NewString()
	external := uuid.NewString()
	session, token, err := auth.NewSession(uuid.NewString(), admin, "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	outside, _, err := auth.NewSession(uuid.NewString(), external, "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := store.CreateUser(ctx, tx, auth.User{ID: admin, PrimaryEmail: "admin@example.test", Role: "administrator",
			MembershipState: "active", CredentialMode: "local"}, "fixture", now); err != nil {
			return err
		}
		if _, err := store.CreateUser(ctx, tx, auth.User{ID: external, PrimaryEmail: "outside@example.test", Role: "member",
			MembershipState: "active", CredentialMode: "external"}, "fixture", now); err != nil {
			return err
		}
		if err := store.SaveSession(ctx, tx, session); err != nil {
			return err
		}
		if err := store.SaveSession(ctx, tx, outside); err != nil {
			return err
		}
		_, err := config.RecordInstallationSettings(ctx, tx, config.Activation{InstallationID: installation, ActorID: admin, ActivatedAt: now}, config.InstallationSettings{
			BrowserCertificateID: "browser", OrganizationName: "ShiftBox", DataProfile: "sqlite", ManagementOrigin: origin,
			IDEHostPattern: "{workspace}.ide.test", PreviewHostPattern: "{service}--{workspace}.preview.test", ExternalAuthIntervalSeconds: 14400,
		})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE installations SET state='active' WHERE id=$1", installation)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := &proxy.Registry{}
	closed := false
	if _, err := registry.Register(proxy.Scope{UserID: external, SessionID: outside.ID}, func() error { return nil }, func() error {
		closed = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	dex := &dexConnectorFixture{stageErr: errors.New("private-fixture-secret")}
	service := externalConfiguration{db: db, origin: origin, registry: registry, dex: dex,
		discover: func(context.Context, identity.ExternalConnectorConfig) error { return nil }, now: func() time.Time { return now }}
	handler := service.handler()
	send := func(version, body string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, origin+"/api/v1/admin/identity/external", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("If-Match", version)
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != want || strings.Contains(w.Body.String(), "private-fixture-secret") {
			t.Fatalf("configuration response = %d, want %d and no credential display", w.Code, want)
		}
	}
	const enable = `{"enabled":true,"issuer":"https://upstream.test","connectorId":"company","clientId":"fixture","clientSecret":"private-fixture-secret","authorizationIntervalSeconds":3600}`
	send(`"1"`, enable, 503)
	if _, err := CurrentExternalConnector(ctx, db); !errors.Is(err, sql.ErrNoRows) || closed {
		t.Fatal("failed staging changed live identity authority")
	}
	dex.stageErr = nil
	dex.deleted = nil
	send(`"1"`, enable, 204)
	got, err := CurrentExternalConnector(ctx, db)
	if err != nil || got.Issuer != "https://upstream.test" || got.ID != "company" || got.DexID != dex.staged || !closed {
		t.Fatalf("connector activation or connection closure failed: %v", err)
	}
	send(`"1"`, enable, 412)
	if _, err := db.ExecContext(ctx, "UPDATE users SET must_change_password=1 WHERE id=$1", admin); err != nil {
		t.Fatal(err)
	}
	// A separate external administrator can make changes, but disabling
	// the connector must preserve a usable local administrator.
	if _, err := db.ExecContext(ctx, "UPDATE users SET role='administrator' WHERE id=$1", external); err != nil {
		t.Fatal(err)
	}
	third, externalToken, err := auth.NewSession(uuid.NewString(), external, "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	third.ExternalAuthorizedAt = &now
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error { return store.SaveSession(ctx, tx, third) }); err != nil {
		t.Fatal(err)
	}
	localToken := token
	token = externalToken
	send(`"2"`, `{"enabled":false,"authorizationIntervalSeconds":14400}`, 409)
	token = localToken
	if _, err := db.ExecContext(ctx, "UPDATE users SET must_change_password=0 WHERE id=$1", admin); err != nil {
		t.Fatal(err)
	}
	send(`"2"`, `{"enabled":false,"authorizationIntervalSeconds":14400}`, 204)
	if _, err := CurrentExternalConnector(ctx, db); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("disabled connector lookup = %v, want no row", err)
	}
	if len(dex.deleted) != 1 || dex.deleted[0] != got.DexID {
		t.Fatal("disable did not remove the previous Dex connector")
	}
}
