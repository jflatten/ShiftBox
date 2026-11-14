package backup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestManagementArchiveRoundTrip checks a closed archive with the real age
// scrypt format, including whole-stream validation and archive exclusions.
func TestManagementArchiveRoundTrip(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	source := filepath.Join(dir, "db")
	if err := os.WriteFile(source, []byte("management database contents"), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "fixture.age")
	password := "test-only distinct archive password"
	metadata := ArchiveMetadata{InstallationID: "fixture", Release: "development", Profile: "sqlite", Schema: 18}
	sources := map[string]string{"application.db": source, "dex.db": source,
		"configuration.json": source, "installation.key": source, "guest-ca.json": source}
	evidence, err := CreateArchive(ctx, output, password, metadata, sources)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(password)) || bytes.Contains(raw, []byte("management database contents")) ||
		!bytes.HasPrefix(raw, []byte("age-encryption.org/v1")) {
		t.Fatal("backup did not retain only encrypted management contents")
	}
	got, err := VerifyArchive(ctx, bytes.NewReader(raw), password)
	if err != nil || got != evidence || got.Metadata != metadata || got.Size != int64(len(raw)) {
		t.Fatalf("verified archive = %+v, %v; want recorded archive evidence", got, err)
	}
	if _, err := VerifyArchive(ctx, bytes.NewReader(raw[:len(raw)-1]), password); err == nil {
		t.Error("accepted truncated archive")
	}
	corrupt := bytes.Clone(raw)
	corrupt[len(corrupt)-1] ^= 1
	if _, err := VerifyArchive(ctx, bytes.NewReader(corrupt), password); err == nil {
		t.Error("accepted altered ciphertext")
	}
	if _, err := VerifyArchive(ctx, bytes.NewReader(raw), "other password"); err == nil {
		t.Error("accepted incorrect archive password")
	}
	if _, err := CreateArchive(ctx, output, password, metadata, sources); err == nil {
		t.Error("accepted replacing an existing archive")
	}
	after, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("existing archive changed after collision")
	}
	sources["workspace.utm/disk.qcow2"] = source
	disallowed := filepath.Join(dir, "not-published.age")
	if _, err := CreateArchive(ctx, disallowed, password, metadata, sources); err == nil {
		t.Error("allowed a workspace disk into a management archive")
	}
	if _, err := os.Stat(disallowed); !os.IsNotExist(err) {
		t.Fatal("a disallowed archive was published")
	}
	if len(evidence.ArchiveDigest) != 64 || len(evidence.ManifestDigest) != 64 {
		t.Error("expected two SHA-256 evidence digests")
	}
}

// TestCaptureArchiveLetsProfileAcquireCheckpoint checks that the profile
// controls its write fence and that a failed capture leaves no staged data.
func TestCaptureArchiveLetsProfileAcquireCheckpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.age")
	metadata := ArchiveMetadata{InstallationID: "fixture", Release: "development", Profile: "postgres", Schema: 18}
	failure := errors.New("identity dump interrupted")
	called := false
	_, err := CaptureArchive(t.Context(), path, "test archive password", metadata, func(_ context.Context, stage string) (map[string]string, error) {
		called = true
		if err := os.WriteFile(filepath.Join(stage, "application.dump"), []byte("private test data"), 0600); err != nil {
			t.Fatal(err)
		}
		return nil, failure
	})
	if !called || !errors.Is(err, failure) {
		t.Fatalf("capture = %v, called %t; want the checkpoint error", err, called)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("failed capture left staged content: %v", err)
	}
}
