package acceptance_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/backup"
)

// TestBackupRecoveryJournal verifies the populated recovery fixture after the
// protected restore command and browser approval finish. It reads management
// evidence only, without opening or claiming recovery of a workspace disk.
func TestBackupRecoveryJournal(t *testing.T) {
	sourcePath := os.Getenv("SHIFTBOX_RECOVERY_SOURCE_DATABASE")
	restoredPath := os.Getenv("SHIFTBOX_RECOVERY_RESTORED_DATABASE")
	archivePath := os.Getenv("SHIFTBOX_RECOVERY_ARCHIVE")
	if sourcePath == "" && restoredPath == "" && archivePath == "" {
		t.Skip("set the source database, restored database, and archive after the populated recovery journey")
	}
	for _, path := range []string{sourcePath, restoredPath, archivePath} {
		if !filepath.IsAbs(path) {
			t.Fatal("source database, restored database, and archive paths must be absolute")
		}
	}
	sourceInfo, sourceErr := os.Stat(sourcePath)
	restoredInfo, restoredErr := os.Stat(restoredPath)
	if sourceErr != nil || restoredErr != nil || os.SameFile(sourceInfo, restoredInfo) {
		t.Fatal("recovery evidence requires distinct existing source and restored databases")
	}
	source, restored := openRecoveryEvidence(t, sourcePath), openRecoveryEvidence(t, restoredPath)
	ctx := t.Context()

	archive, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, archive)
	if err != nil {
		t.Fatal(err)
	}
	var installationID string
	err = source.QueryRowContext(ctx, `SELECT i.id FROM installations i JOIN backup_records b
		ON json_extract(b.schema_versions_json,'$.installationId')=i.id JOIN operations o ON o.id=b.operation_id
		WHERE i.state='active' AND b.state='completed' AND o.type='backup' AND o.status='succeeded'
		AND b.archive_digest=$1 AND b.size_bytes=$2 LIMIT 1`, hex.EncodeToString(digest.Sum(nil)), size).Scan(&installationID)
	if err != nil {
		t.Fatalf("source archive digest and size have no verified backup record: %v", err)
	}

	review, err := backup.ReadRecoveryReview(ctx, restored, latestActivatedReview(t, restored))
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Sections.Workspaces) == 0 || len(review.Sections.UserIDs) == 0 {
		t.Fatal("activated recovery requires populated workspace and identity review evidence")
	}
	for _, workspace := range review.Sections.Workspaces {
		if workspace.Match != "matched" || (workspace.Power != "running" && workspace.Power != "stopped") {
			t.Error("activated review contains an unresolved provider observation")
		}
		var sourceOwner, sourceInstance, sourceMarker string
		err := source.QueryRowContext(ctx, `SELECT owner_user_id,COALESCE(provider_instance_id,''),ownership_marker
			FROM workspaces WHERE id=$1 AND generation=$2 AND installation_id=$3`,
			workspace.WorkspaceID, workspace.Generation, installationID).Scan(&sourceOwner, &sourceInstance, &sourceMarker)
		if err != nil || sourceInstance == "" || sourceMarker == "" {
			t.Errorf("retained workspace lacks source identity evidence: %v", err)
			continue
		}
		assertRecoveryEvidence(t, restored, "restored ownership and provider identity match the source", `SELECT EXISTS(
			SELECT 1 FROM workspaces WHERE id=$1 AND generation=$2 AND installation_id=$3
			AND owner_user_id=$4 AND provider_instance_id=$5 AND ownership_marker=$6
		)`, workspace.WorkspaceID, workspace.Generation, installationID, sourceOwner, sourceInstance, sourceMarker)
	}
	assertRecoveryEvidence(t, restored, "provider review covers the retained workspace inventory", `SELECT count(*)=$2
		FROM workspaces WHERE installation_id=$1 AND observed_state<>'deleted'`, installationID, len(review.Sections.Workspaces))
	assertRecoveryEvidence(t, restored, "the same installation entered recovery and explicitly activated", `SELECT EXISTS(
		SELECT 1 FROM installations i JOIN recovery_reviews r JOIN operations o ON o.id=r.restore_operation_id
		JOIN audit_events a ON a.resource_id=r.id AND a.actor_id=r.approved_by
		WHERE i.id=$1 AND i.state='active' AND r.id=$2 AND r.state='activated'
		AND o.type='restore' AND o.originating_action='restore.activate' AND o.status='succeeded'
		AND a.action='recovery.activate' AND a.outcome='activated'
	)`, installationID, review.ID)
	assertRecoveryEvidence(t, restored, "copied authority and the recovery session stayed revoked", `SELECT EXISTS(
		SELECT 1 FROM sessions WHERE revocation_reason='restore' AND revoked_at IS NOT NULL
	) AND EXISTS (
		SELECT 1 FROM sessions WHERE mode='recovery_review' AND revocation_reason='recovery_activated' AND revoked_at IS NOT NULL
	) AND NOT EXISTS (
		SELECT 1 FROM sessions s JOIN recovery_reviews r ON r.id=$1
		WHERE s.revoked_at IS NULL AND s.authenticated_at<r.started_at
	)`, review.ID)
	rows, err := source.QueryContext(ctx, "SELECT id FROM origin_grants WHERE revoked_at IS NULL")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var copiedGrants int
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		var revoked bool
		err := restored.QueryRowContext(ctx, "SELECT revoked_at IS NOT NULL FROM origin_grants WHERE id=$1", id).Scan(&revoked)
		if err == sql.ErrNoRows {
			// A grant minted on the source after the checkpoint wasn't copied.
			continue
		}
		if err != nil || !revoked {
			t.Errorf("a copied origin grant remained usable after restore: %v", err)
		}
		copiedGrants++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if copiedGrants == 0 {
		t.Error("recovery fixture needs a copied active browser origin grant to prove revocation")
	}
}

func openRecoveryEvidence(t *testing.T, path string) *sql.DB {
	t.Helper()
	dsn := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func latestActivatedReview(t *testing.T, db *sql.DB) string {
	t.Helper()
	var reviewID string
	err := db.QueryRowContext(t.Context(), "SELECT id FROM recovery_reviews WHERE state='activated' ORDER BY activated_at DESC LIMIT 1").Scan(&reviewID)
	if err != nil || strings.TrimSpace(reviewID) == "" {
		t.Fatalf("restored database lacks an activated review: %v", err)
	}
	return reviewID
}

func assertRecoveryEvidence(t *testing.T, db *sql.DB, label, query string, args ...any) {
	t.Helper()
	var passed bool
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&passed); err != nil || !passed {
		t.Errorf("%s = %t (%v), want durable evidence", label, passed, err)
	}
}
