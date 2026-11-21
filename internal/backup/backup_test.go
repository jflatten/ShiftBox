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
	checkpointParent := checkpointStage(t)
	path := filepath.Join(dir, "backup.age")
	metadata := ArchiveMetadata{InstallationID: "fixture", Release: "development", Profile: "postgres", Schema: 18}
	failure := errors.New("identity dump interrupted")
	var checkpoint string
	_, err := CaptureArchive(t.Context(), path, "test archive password", metadata, checkpointParent, func(_ context.Context, stage string) (map[string]string, error) {
		checkpoint = stage
		if err := os.WriteFile(filepath.Join(stage, "application.dump"), []byte("private test data"), 0600); err != nil {
			t.Fatal(err)
		}
		return nil, failure
	})
	if checkpoint == "" || !errors.Is(err, failure) {
		t.Fatalf("capture = %v, checkpoint set %t; want the checkpoint error", err, checkpoint != "")
	}
	if filepath.Dir(checkpoint) != checkpointParent {
		t.Error("plaintext checkpoint was staged outside the controller's private directory")
	}
	if _, err := os.Lstat(checkpoint); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("checkpoint directory still exists or could not be checked: %v", err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("failed capture left staged content: %v", err)
	}
}

// TestCaptureArchiveRejectsChangedDestination checks that changing the
// operator-selected directory during capture cannot redirect publication.
func TestCaptureArchiveRejectsChangedDestination(t *testing.T) {
	parent := t.TempDir()
	outputDir := filepath.Join(parent, "exports")
	if err := os.Mkdir(outputDir, 0700); err != nil {
		t.Fatal(err)
	}
	movedDir := filepath.Join(parent, "previous-exports")
	output := filepath.Join(outputDir, "management.age")
	metadata := ArchiveMetadata{InstallationID: "fixture", Release: "development", Profile: "sqlite", Schema: 18}
	_, err := CaptureArchive(t.Context(), output, "test archive password", metadata, checkpointStage(t), func(_ context.Context, stage string) (map[string]string, error) {
		source := filepath.Join(stage, "checkpoint")
		if err := os.WriteFile(source, []byte("fixture data"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(outputDir, movedDir); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(outputDir, 0700); err != nil {
			t.Fatal(err)
		}
		return map[string]string{"application.db": source, "dex.db": source,
			"configuration.json": source, "installation.key": source, "guest-ca.json": source}, nil
	})
	if err == nil {
		t.Error("capture reported success after the destination directory changed")
	}
	for _, directory := range []string{outputDir, movedDir} {
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			t.Errorf("%s after failed capture = %v, %v; want an empty directory", directory, entries, err)
		}
	}
}

// TestCreateArchiveAcceptsDirectoryAlias preserves an operator's legitimate
// absolute output path through a symlink to an existing directory.
func TestCreateArchiveAcceptsDirectoryAlias(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "exports")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "checkpoint")
	if err := os.WriteFile(source, []byte("fixture data"), 0600); err != nil {
		t.Fatal(err)
	}
	metadata := ArchiveMetadata{InstallationID: "fixture", Release: "development", Profile: "sqlite", Schema: 18}
	sources := map[string]string{"application.db": source, "dex.db": source,
		"configuration.json": source, "installation.key": source, "guest-ca.json": source}
	if _, err := CreateArchive(t.Context(), filepath.Join(alias, "management.age"), "test archive password", metadata, sources); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "management.age"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Errorf("archive mode = %v; want a private regular file", info.Mode())
	}
}
