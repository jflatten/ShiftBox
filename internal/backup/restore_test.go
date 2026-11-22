package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
)

const restoreFixturePassword = "fixture-only archive passphrase"

func restoreFixtureMetadata() ArchiveMetadata {
	return ArchiveMetadata{
		InstallationID: "00000000-0000-4000-8000-000000000023",
		HostID:         "fixture-original-mac",
		Release:        "v1.2.3",
		Profile:        "sqlite",
		Schema:         19,
	}
}

// TestValidateRestoreRejectsArchivesBeforeTargetChecks verifies that archive
// integrity, compatibility, and mounted-key failures stop validation before
// it contacts or changes the proposed replacement target.
func TestValidateRestoreRejectsArchivesBeforeTargetChecks(t *testing.T) {
	metadata := restoreFixtureMetadata()
	key := bytes.Repeat([]byte{1}, 32)
	archive := makeRestoreFixture(t, metadata, key, false)
	withBadEntryDigest := makeRestoreFixture(t, metadata, key, true)
	withShortKey := makeRestoreFixture(t, metadata, key[:31], false)
	type restoreCase struct {
		name     string
		archive  []byte
		password string
		required RestoreRequirements
		want     error
	}
	tests := []restoreCase{
		{name: "wrong password", archive: archive, password: "different fixture password",
			required: RestoreRequirements{Metadata: metadata}, want: errArchiveInvalid},
		{name: "truncated ciphertext", archive: archive[:len(archive)-1], password: restoreFixturePassword,
			required: RestoreRequirements{Metadata: metadata}, want: errArchiveInvalid},
		{name: "wrong authenticated entry digest", archive: withBadEntryDigest, password: restoreFixturePassword,
			required: RestoreRequirements{Metadata: metadata}, want: errArchiveInvalid},
		{name: "different archive digest", archive: archive, password: restoreFixturePassword,
			required: RestoreRequirements{Metadata: metadata, ArchiveDigest: digestBytes([]byte("another archive"))}},
		{name: "mismatched mounted key", archive: archive, password: restoreFixturePassword,
			required: RestoreRequirements{Metadata: metadata, MountedKey: bytes.Repeat([]byte{2}, 32)}, want: ErrRestoreKey},
		{name: "invalid archived key", archive: withShortKey, password: restoreFixturePassword,
			required: RestoreRequirements{Metadata: metadata}, want: ErrRestoreKey},
	}
	candidates := []struct {
		name   string
		change func(*ArchiveMetadata)
	}{
		{name: "wrong host", change: func(m *ArchiveMetadata) { m.HostID = "different-mac" }},
		{name: "different installation", change: func(m *ArchiveMetadata) { m.InstallationID = "other-installation" }},
		{name: "incompatible release", change: func(m *ArchiveMetadata) { m.Release = "v2.0.0" }},
		{name: "incompatible schema", change: func(m *ArchiveMetadata) { m.Schema++ }},
		{name: "different database profile", change: func(m *ArchiveMetadata) { m.Profile = "postgres" }},
	}
	for _, candidate := range candidates {
		expected := metadata
		candidate.change(&expected)
		tests = append(tests, restoreCase{
			name: candidate.name, archive: archive, password: restoreFixturePassword,
			required: RestoreRequirements{Metadata: expected}, want: ErrRestoreCompatibility,
		})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateRestore(t.Context(), bytes.NewReader(tt.archive), tt.password, tt.required,
				func(context.Context) error {
					t.Error("target inspection started before archive checks passed")
					return nil
				})
			if err == nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("ValidateRestore(%s) error = %v, want validation failure %v", tt.name, err, tt.want)
			}
			if got.Evidence.ArchiveDigest != "" || got.KeyDisposition != "" {
				t.Errorf("ValidateRestore(%s) returned authorization evidence for a failed validation", tt.name)
			}
		})
	}
}

// TestValidateRestoreChecksReplacementWithoutMutation checks that validation
// rejects a populated or unowned replacement and leaves both inputs intact.
func TestValidateRestoreChecksReplacementWithoutMutation(t *testing.T) {
	metadata := restoreFixtureMetadata()
	key := bytes.Repeat([]byte{3}, 32)
	archive := makeRestoreFixture(t, metadata, key, false)
	replacement := t.TempDir()
	sentinel := filepath.Join(replacement, "retained-file")
	original := []byte("existing target content")
	if err := os.WriteFile(sentinel, original, 0600); err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{"non-empty target", "target is owned by another role"} {
		t.Run(reason, func(t *testing.T) {
			calls := 0
			_, err := ValidateRestore(t.Context(), bytes.NewReader(archive), restoreFixturePassword,
				RestoreRequirements{Metadata: metadata, MountedKey: key}, func(context.Context) error {
					calls++
					return errors.New(reason)
				})
			if !errors.Is(err, ErrRestoreTarget) || calls != 1 {
				t.Errorf("ValidateRestore(%s) = %v, target checks = %d, want ErrRestoreTarget and one check", reason, err, calls)
			}
			entries, err := os.ReadDir(replacement)
			if err != nil || len(entries) != 1 {
				t.Fatalf("ValidateRestore(%s) changed replacement directory entries = %v, %v", reason, entries, err)
			}
			actual, err := os.ReadFile(sentinel)
			if err != nil || !bytes.Equal(actual, original) {
				t.Errorf("ValidateRestore(%s) changed the existing target file, read error = %v", reason, err)
			}
		})
	}
}

// TestValidateRestoreReturnsVerifiedEvidence checks both key dispositions and
// proves validation consumes the complete stream before invoking target checks.
func TestValidateRestoreReturnsVerifiedEvidence(t *testing.T) {
	metadata := restoreFixtureMetadata()
	key := bytes.Repeat([]byte{4}, 32)
	archive := makeRestoreFixture(t, metadata, key, false)
	wantDigest := digestBytes(archive)
	var dispositions []string
	for _, mounted := range [][]byte{nil, key} {
		reader := bytes.NewReader(archive)
		targetChecks := 0
		result, err := ValidateRestore(t.Context(), reader, restoreFixturePassword,
			RestoreRequirements{Metadata: metadata, MountedKey: mounted, ArchiveDigest: wantDigest},
			func(context.Context) error {
				targetChecks++
				if reader.Len() != 0 {
					t.Error("target inspection started before the complete archive was read")
				}
				return nil
			})
		if err != nil {
			t.Fatalf("ValidateRestore with mounted key %t = %v, want a verified result", mounted != nil, err)
		}
		evidence := result.Evidence
		if evidence.Metadata != metadata || evidence.ArchiveDigest != wantDigest ||
			evidence.Size != int64(len(archive)) || !archiveDigest(evidence.ManifestDigest) || targetChecks != 1 {
			t.Errorf("ValidateRestore with mounted key %t returned incomplete evidence or target checks = %d", mounted != nil, targetChecks)
		}
		if result.KeyDisposition == "" {
			t.Errorf("ValidateRestore with mounted key %t returned no key disposition", mounted != nil)
		}
		dispositions = append(dispositions, result.KeyDisposition)
	}
	if dispositions[0] == dispositions[1] {
		t.Errorf("key dispositions for archived and mounted keys are identical: %q", dispositions[0])
	}
}

func makeRestoreFixture(t *testing.T, metadata ArchiveMetadata, key []byte, badDigest bool) []byte {
	t.Helper()
	doc := manifest{Version: archiveVersion, Metadata: metadata}
	files := make(map[string][]byte)
	for _, name := range archiveNames(metadata.Profile) {
		payload := []byte("fixture " + name)
		if name == "installation.key" {
			payload = key
		}
		digest := digestBytes(payload)
		if name == "dex.db" && badDigest {
			digest = digestBytes([]byte("different data"))
		}
		doc.Files = append(doc.Files, archiveFile{Name: name, Size: int64(len(payload)), Digest: digest})
		files[name] = payload
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := age.NewScryptRecipient(restoreFixturePassword)
	if err != nil {
		t.Fatal(err)
	}
	recipient.SetWorkFactor(10)
	var output bytes.Buffer
	encrypted, err := age.Encrypt(&output, recipient)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(encrypted)
	writeRestoreFixtureEntry(t, writer, manifestName, data)
	for _, item := range doc.Files {
		writeRestoreFixtureEntry(t, writer, item.Name, files[item.Name])
	}
	if err := errors.Join(writer.Close(), encrypted.Close()); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func writeRestoreFixtureEntry(t *testing.T, writer *tar.Writer, name string, data []byte) {
	t.Helper()
	if err := writer.WriteHeader(archiveHeader(name, int64(len(data)))); err != nil {
		t.Fatal(err)
	}
	if n, err := writer.Write(data); err != nil || n != len(data) {
		t.Fatalf("write archive fixture %s = %d, %v, want %d bytes", name, n, err, len(data))
	}
}
