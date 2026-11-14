package guest_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/guest"
)

// TestEnrollmentSeedRejectsInvalidInputs checks that a bad endpoint, grant,
// or trust anchor fails before a delivery payload is written.
func TestEnrollmentSeedRejectsInvalidInputs(t *testing.T) {
	ca, _, _, _ := authorityFixture(t)
	grant := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	valid := guest.EnrollmentSeed{Grant: grant, Endpoint: "https://internal.test:8443/internal/v1/enroll", PollEndpoint: "https://internal.test:9443/internal/v1/guest/poll", CAPEM: string(ca)}
	for _, seed := range []guest.EnrollmentSeed{
		{Grant: grant, Endpoint: "http://internal.test/internal/v1/enroll", PollEndpoint: valid.PollEndpoint, CAPEM: string(ca)},
		{Grant: grant, Endpoint: "https://operator:secret@internal.test/internal/v1/enroll", PollEndpoint: valid.PollEndpoint, CAPEM: string(ca)},
		{Grant: grant, Endpoint: "https://internal.test/internal/v1/enroll?secret=1", PollEndpoint: valid.PollEndpoint, CAPEM: string(ca)},
		{Grant: "short", Endpoint: valid.Endpoint, PollEndpoint: valid.PollEndpoint, CAPEM: string(ca)},
		{Grant: grant, Endpoint: valid.Endpoint, PollEndpoint: valid.PollEndpoint, CAPEM: "invalid"},
		{Grant: grant, Endpoint: valid.Endpoint, PollEndpoint: "http://internal.test/internal/v1/guest/poll", CAPEM: string(ca)},
		{Grant: grant, Endpoint: valid.Endpoint, PollEndpoint: valid.Endpoint, CAPEM: string(ca)},
	} {
		path := t.TempDir()
		if _, err := guest.BuildEnrollmentMedia(context.Background(), path, seed); !errors.Is(err, guest.ErrInvalidEnrollmentSeed) {
			t.Errorf("BuildEnrollmentMedia invalid input = %v, want invalid seed", err)
		}
		if files, err := os.ReadDir(path); err != nil || len(files) != 0 {
			t.Errorf("invalid seed left files: %v, %v", files, err)
		}
	}
}

// TestEnrollmentMediaContainsOnlySeed verifies real ISO creation, private
// file permissions and an independent image for each attempt.
func TestEnrollmentMediaContainsOnlySeed(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("SHIFTBOX_TEST_ENROLLMENT_MEDIA") == "" {
		t.Skip("requires native macOS enrollment media test")
	}
	ca, key, _, _ := authorityFixture(t)
	root := t.TempDir()
	seed := guest.EnrollmentSeed{
		Grant:    base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32)),
		Endpoint: "https://internal.test:8443/internal/v1/enroll", PollEndpoint: "https://internal.test:9443/internal/v1/guest/poll", CAPEM: string(ca),
	}
	for range 2 {
		path, err := guest.BuildEnrollmentMedia(context.Background(), root, seed)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		dir, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 || dir.Mode().Perm() != 0700 {
			t.Errorf("media permissions = %s, directory = %s", info.Mode(), dir.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{seed.Grant, seed.Endpoint, seed.PollEndpoint, "ENROLLMENT.JSON", "SHIFTBOX"} {
			if !bytes.Contains(data, []byte(value)) {
				t.Errorf("ISO missing expected payload field or label")
			}
		}
		if bytes.Contains(data, []byte("PRIVATE KEY")) || bytes.Contains(data, key) {
			t.Error("ISO contains private key material")
		}
		files, err := os.ReadDir(filepath.Dir(path))
		if err != nil || len(files) != 1 || !strings.HasSuffix(path, ".iso") {
			t.Errorf("media directory = %v, %v, want only ISO", files, err)
		}
	}
	dirs, err := os.ReadDir(root)
	if err != nil || len(dirs) != 2 {
		t.Errorf("delivery directories = %v, %v, want separate attempts", dirs, err)
	}
}
