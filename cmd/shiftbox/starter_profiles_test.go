package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/ec2"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// TestStarterProfilesReachTemplateCatalog verifies that a newly activated AWS
// install offers all three sizes on its exact release AMI and respects later edits.
func TestStarterProfilesReachTemplateCatalog(t *testing.T) {
	t.Run("published image", func(t *testing.T) { verifyStarterProfiles(t, false) })
	t.Run("private Ohio test image", func(t *testing.T) { verifyStarterProfiles(t, true) })
}

func verifyStarterProfiles(t *testing.T, privateTest bool) {
	t.Helper()
	db, active, actor, now := starterProfileDatabase(t)
	settings, raw := cloudCandidateFixture()
	if privateTest {
		const version = "v0.0.0-rc.1234"
		settings.AccountID, settings.Region = "888293065132", "us-east-2"
		pin := settings.OfficialImage
		pin.PublisherAccountID, pin.ReleaseID, pin.GuestRelease = settings.AccountID, version, version
		raw = strings.NewReplacer("us-west-2", settings.Region, "v1.4.2-rc.1", version).Replace(raw)
		pin.CatalogSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
		if _, err := db.Exec("UPDATE installations SET active_release=$1,min_guest_release=$1", version); err != nil {
			t.Fatal(err)
		}
		active.ActiveRelease, active.MinimumGuestRelease = version, version
	}
	settings.StarterCatalog = []byte(raw)
	caps := starterProfileCapabilities()
	observations := 0
	inspect := func(context.Context) (ec2.Image, error) {
		observations++
		return ec2.Image{ID: settings.OfficialImage.AMIID, Architecture: "amd64", RootDeviceName: "/dev/sda1",
			SnapshotID: settings.OfficialImage.RootSnapshotID}, nil
	}
	p := cloudEC2{settings: settings}
	seed := func() {
		t.Helper()
		if err := p.seedStarterProfiles(t.Context(), db, active, caps, now, inspect); err != nil {
			t.Fatal(err)
		}
	}
	seed()
	session, token, err := auth.NewSession(uuid.NewString(), actor, "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(t.Context(), db, func(tx *sql.Tx) error {
		return auth.NewStore(db).SaveSession(t.Context(), tx, session)
	}); err != nil {
		t.Fatal(err)
	}
	handler := web.TemplateCatalogHandler(db, active.Settings.ManagementOrigin, func() time.Time { return now },
		web.WorkspaceLaunchPolicy{Capabilities: caps, Protocols: []string{"1"}, EC2VerifiedAMIs: []string{settings.OfficialImage.AMIID}})
	request := httptest.NewRequest(http.MethodGet, active.Settings.ManagementOrigin+"/api/v1/templates/catalog", nil)
	request.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var catalog struct {
		DefaultPlatformArtifactID string
		ResourceProfiles          []struct{ ID, Name, PlatformArtifactID string }
		PlatformImages            []struct{ ID, AMIID string }
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &catalog); err != nil || recorder.Code != 200 ||
		len(catalog.ResourceProfiles) != 3 || len(catalog.PlatformImages) != 1 {
		t.Fatalf("starter template catalog = %d %s, want three profiles and one image", recorder.Code, recorder.Body)
	}
	image := catalog.PlatformImages[0]
	if image.AMIID != settings.OfficialImage.AMIID || image.ID != catalog.DefaultPlatformArtifactID {
		t.Fatalf("starter image = %+v, want default %s", image, settings.OfficialImage.AMIID)
	}
	expected := map[string]workspace.Allocation{
		"Small":  {CPU: 2, MemoryMiB: 4096, DiskMiB: 32 * 1024, InstanceType: "t3.medium"},
		"Medium": {CPU: 2, MemoryMiB: 8192, DiskMiB: 64 * 1024, InstanceType: "m6a.large"},
		"Large":  {CPU: 4, MemoryMiB: 16384, DiskMiB: 96 * 1024, InstanceType: "m6a.xlarge"},
	}
	for _, choice := range catalog.ResourceProfiles {
		profile, err := workspace.NewStore(db).ResourceProfile(t.Context(), choice.ID)
		if err != nil || !profile.Enabled || profile.PlatformArtifactID != image.ID ||
			choice.PlatformArtifactID != image.ID || profile.Allocation != expected[choice.Name] {
			t.Errorf("starter profile %q = %+v (%v), want enabled %+v with AMI %s", choice.Name, profile, err, expected[choice.Name], image.ID)
		}
	}
	if _, err := db.Exec(`UPDATE resource_profiles SET name='Developer',enabled=0,version=2 WHERE name='Small';
		DELETE FROM resource_profiles WHERE name='Large'`); err != nil {
		t.Fatal(err)
	}
	seed()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM resource_profiles WHERE name='Small' OR name='Large' OR (name='Developer' AND enabled=1)").Scan(&count); err != nil || count != 0 || observations != 1 {
		t.Fatalf("restart changed %d edited profiles or reinspected AMI %d times (%v), want 0 and 1", count, observations, err)
	}
}

// TestStarterProfilesRejectUnverifiedAMI leaves the initial catalog empty if
// the runtime catalog bytes, observed snapshot, or available compute differ.
func TestStarterProfilesRejectUnverifiedAMI(t *testing.T) {
	db, active, _, now := starterProfileDatabase(t)
	settings, raw := cloudCandidateFixture()
	inspect := func(context.Context) (ec2.Image, error) {
		return ec2.Image{ID: settings.OfficialImage.AMIID, Architecture: "amd64", RootDeviceName: "/dev/sda1",
			SnapshotID: "snap-0123456789abcdef1"}, nil
	}
	caps := starterProfileCapabilities()
	for _, input := range []string{strings.TrimSpace(raw), raw} {
		settings.StarterCatalog = []byte(input)
		p := cloudEC2{settings: settings}
		if err := p.seedStarterProfiles(t.Context(), db, active, caps, now, inspect); !errors.Is(err, release.ErrInvalidArtifact) {
			t.Fatalf("seedStarterProfiles with invalid bytes or snapshot = %v, want invalid artifact", err)
		}
	}
	settings.StarterCatalog = []byte(raw)
	p := cloudEC2{settings: settings}
	if err := p.seedStarterProfiles(t.Context(), db, active, caps, now, func(context.Context) (ec2.Image, error) {
		return ec2.Image{}, provider.ErrUnavailable
	}); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatalf("seedStarterProfiles with unavailable AWS = %v, want provider unavailable", err)
	}
	caps.InstanceTypes = caps.InstanceTypes[:2]
	if err := p.seedStarterProfiles(t.Context(), db, active, caps, now, func(context.Context) (ec2.Image, error) {
		return ec2.Image{ID: settings.OfficialImage.AMIID, Architecture: "amd64", RootDeviceName: "/dev/sda1",
			SnapshotID: settings.OfficialImage.RootSnapshotID}, nil
	}); !errors.Is(err, provider.ErrCapabilityUnsupported) {
		t.Fatalf("seedStarterProfiles without a compatible Large type = %v, want provider capability unsupported", err)
	}
	var count int
	if err := db.QueryRow("SELECT (SELECT count(*) FROM resource_profiles)+(SELECT count(*) FROM platform_artifacts)").Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed seed created %d catalog rows (%v), want 0", count, err)
	}
}

func starterProfileCapabilities() provider.Capabilities {
	return provider.Capabilities{Name: "ec2", Architecture: "amd64", Create: true, Start: true,
		Minimum: provider.Resources{DiskBytes: 32 << 30}, Maximum: provider.Resources{DiskBytes: 2047 << 30},
		InstanceTypes: []provider.InstanceType{
			{Name: "t3.medium", Architecture: "amd64", CPUCount: 2, MemoryMiB: 4096},
			{Name: "m6a.large", Architecture: "amd64", CPUCount: 2, MemoryMiB: 8192},
			{Name: "m6a.xlarge", Architecture: "amd64", CPUCount: 4, MemoryMiB: 16384},
		}}
}

func starterProfileDatabase(t *testing.T) (*sql.DB, app.ActiveInstallation, string, time.Time) {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	seed := database.BootstrapSeed{ID: uuid.NewString(), Organization: "ShiftBox", Origin: "https://shiftbox.example.test",
		IDEPattern: "{workspace}.ide.example.test", PreviewPattern: "{service}--{workspace}.preview.example.test",
		Release: "v1.4.2-rc.1", MinGuestRelease: "v1.4.2-rc.1", Now: now}
	db, id, err := database.OpenBootstrap(ctx, filepath.Join(t.TempDir(), "setup.db"), seed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	actor := uuid.NewString()
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		_, err := auth.NewStore(db).CreateUser(ctx, tx, auth.User{ID: actor, PrimaryEmail: "owner@example.test",
			Role: "administrator", MembershipState: "active", CredentialMode: "local"}, "local_setup", now)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE installations SET state='active',config_generation=1 WHERE id=$1`, id); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO activated_configurations(installation_id,generation,settings_json,activated_by,activated_at)
			VALUES($1,1,'{}',$2,$3)`, id, actor, now.Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	active := app.ActiveInstallation{InstallationID: id, Generation: 1, ActiveRelease: seed.Release, MinimumGuestRelease: seed.MinGuestRelease}
	active.Settings.ManagementOrigin = seed.Origin
	return db, active, actor, now
}
