package release_test

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/release"
	_ "modernc.org/sqlite"
)

// TestMVPReleaseManifest verifies the candidate inputs can be read by the
// same manifest parser used by migration preflight.
func TestMVPReleaseManifest(t *testing.T) {
	readMVPRelease(t)
}

// TestMVPReleaseManifestOverride lets a frozen candidate keep a distinct
// manifest while the working branch still points at its previous build.
func TestMVPReleaseManifestOverride(t *testing.T) {
	t.Setenv("SHIFTBOX_TEST_MVP_RELEASE", "")
	raw, err := os.ReadFile("../../platform/manifests/release.json")
	if err != nil {
		t.Fatal(err)
	}
	original := readMVPRelease(t).ReleaseID
	const candidate = "fixture-candidate-override"
	raw = []byte(strings.Replace(string(raw), `"releaseId": "`+original+`"`, `"releaseId": "`+candidate+`"`, 1))
	path := filepath.Join(t.TempDir(), "release.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHIFTBOX_TEST_MVP_RELEASE", path)
	if got := readMVPRelease(t).ReleaseID; got != candidate {
		t.Errorf("manifest release = %q, want %q", got, candidate)
	}
}

// TestMVPWorkspaceJournal checks controller-side evidence from a fresh
// exact-stack browser run. Run it against the same disposable SQLite database
// after the browser journey finishes and before deleting the fixture.
func TestMVPWorkspaceJournal(t *testing.T) {
	dbPath := os.Getenv("SHIFTBOX_TEST_MVP_DATABASE")
	if dbPath == "" {
		t.Skip("set SHIFTBOX_TEST_MVP_DATABASE to the completed browser fixture database")
	}
	if !filepath.IsAbs(dbPath) {
		t.Fatal("SHIFTBOX_TEST_MVP_DATABASE must be an absolute path")
	}
	dsn := url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	manifest := readMVPRelease(t)
	ctx := t.Context()
	var workspaceID, observed, guest, protocol, artifact, activeRelease string
	err = db.QueryRowContext(ctx, `SELECT w.id,w.observed_state,COALESCE(w.guest_release,''),
		COALESCE(w.protocol_version,''),a.state,i.active_release
		FROM workspaces w JOIN platform_artifacts a ON a.id=w.platform_artifact_id
		JOIN installations i ON i.id=w.installation_id
		WHERE i.state='active' AND w.desired_state='running'
		AND (SELECT count(*) FROM workspaces)=1`).
		Scan(&workspaceID, &observed, &guest, &protocol, &artifact, &activeRelease)
	if err != nil {
		t.Fatalf("fresh fixture workspace query = %v, want one running workspace in an active installation", err)
	}
	if observed != "running" || activeRelease != manifest.ReleaseID || guest != manifest.Guest.Version ||
		protocol != manifest.ProtocolVersion || artifact != "available" {
		t.Errorf("workspace state, active release, guest release, protocol and artifact = %q, %q, %q, %q, %q; want running, %q, %q, %q, available",
			observed, activeRelease, guest, protocol, artifact, manifest.ReleaseID, manifest.Guest.Version, manifest.ProtocolVersion)
	}
	var enrolled bool
	err = db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM guest_registrations g JOIN enrollment_grants e
		ON e.workspace_id=g.workspace_id AND e.generation=g.generation
		WHERE g.workspace_id=$1 AND g.state='active' AND g.last_authenticated_at IS NOT NULL
		AND e.state='consumed')`, workspaceID).Scan(&enrolled)
	if err != nil || !enrolled {
		t.Errorf("authenticated workspace enrollment = %v (%v), want a consumed grant and active guest", enrolled, err)
	}

	rows, err := db.QueryContext(ctx, `SELECT o.originating_action,o.status,s.step_key,s.status
		FROM operations o JOIN operation_steps s ON s.operation_id=o.id
		WHERE o.workspace_id=$1 AND o.type IN ('provision','stop','start','rebuild','repair')
		ORDER BY o.created_at,o.id,s.step_index`, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var steps []string
	for rows.Next() {
		var action, operationStatus, step, status string
		if err := rows.Scan(&action, &operationStatus, &step, &status); err != nil {
			t.Fatal(err)
		}
		if step == "verify_ide" || step == "verify_repair" || step == "stop" || (step == "configure" && status == "failed") {
			steps = append(steps, strings.Join([]string{action, operationStatus, step, status}, "/"))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"workspace.launch/succeeded/verify_ide/succeeded",
		"workspace.stop/succeeded/stop/succeeded",
		"workspace.start/succeeded/verify_ide/succeeded",
		"workspace.rebuild/succeeded/verify_ide/succeeded",
		"workspace.rebuild/failed/configure/failed",
		"workspace.rebuild/failed/verify_ide/pending",
		"workspace.repair/succeeded/verify_repair/succeeded",
		"workspace.repair/succeeded/verify_ide/succeeded",
	}
	if !matchesWorkspaceJourney(steps, want, os.Getenv("SHIFTBOX_MVP_RESUME_ACTIVE") == "1") {
		got := strings.Join(steps, "\n")
		t.Errorf("workspace lifecycle checks =\n%s\nwant\n%s", got, strings.Join(want, "\n"))
	}
}

// TestMatchesWorkspaceJourney checks resumed fixtures retain the ordered
// lifecycle evidence despite older retries and reject an unfinished repair.
func TestMatchesWorkspaceJourney(t *testing.T) {
	want := []string{"launch", "stop", "start", "rebuild", "failed rebuild", "repair", "retry"}
	resumed := []string{"launch", "stop", "start", "stop", "start", "rebuild", "rebuild", "failed rebuild", "repair", "retry"}
	if matchesWorkspaceJourney(resumed, want, false) {
		t.Error("fresh journal accepted earlier attempts")
	}
	if !matchesWorkspaceJourney(resumed, want, true) {
		t.Error("resumed journal rejected ordered lifecycle evidence")
	}
	if matchesWorkspaceJourney(resumed[:len(resumed)-1], want, true) ||
		matchesWorkspaceJourney(append(resumed, "failed rebuild"), want, true) ||
		matchesWorkspaceJourney([]string{"launch", "stop", "start", "rebuild", "repair", "failed rebuild", "retry"}, want, true) {
		t.Error("resumed journal accepted missing, superseded, or out-of-order repair evidence")
	}
}

func matchesWorkspaceJourney(steps, want []string, resumed bool) bool {
	if !resumed || len(want) == 0 {
		return slices.Equal(steps, want)
	}
	if len(steps) < len(want) || steps[len(steps)-1] != want[len(want)-1] {
		return false
	}
	index := 0
	for _, step := range steps[:len(steps)-1] {
		if index < len(want)-1 && step == want[index] {
			index++
		}
	}
	return index == len(want)-1
}

func readMVPRelease(t *testing.T) release.MigrationRelease {
	t.Helper()
	path := os.Getenv("SHIFTBOX_TEST_MVP_RELEASE")
	if path == "" {
		path = "../../platform/manifests/release.json"
	} else if !filepath.IsAbs(path) {
		t.Fatal("SHIFTBOX_TEST_MVP_RELEASE must be an absolute path")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	manifest, err := release.ReadMigrationRelease(file)
	if err != nil {
		t.Fatalf("parse candidate release manifest: %v", err)
	}
	return manifest
}
