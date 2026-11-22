package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/release"
)

const officialCatalogFixture = `{
  "builds": [{
    "name": "shiftbox",
    "builder_type": "amazon-ebs",
    "artifact_id": "us-west-2:ami-0123456789abcdef0",
    "packer_run_uuid": "6e005968-cfd0-4f74-b290-361c25f5ac71",
    "custom_data": {
      "provider": "ec2",
      "region": "us-west-2",
      "sourceRegion": "us-east-2",
      "sourceAmiId": "ami-0123456789abcdef1",
      "sourceAmiOwnerId": "099720109477",
      "sourceRevision": "aabbccddeeff00112233445566778899aabbccdd",
      "releaseId": "v1.4.2-rc.1",
      "architecture": "amd64",
      "platformVersion": "24.04",
      "protocolVersion": "1",
      "minimumRootDiskGiB": "32",
      "guestAgentRelease": "v1.4.2-rc.1",
      "guestAgentSHA256": "DIGEST",
      "guestUpdateKeySHA256": "DIGEST",
      "toolchainRecipeSHA256": "DIGEST",
      "codeServerRecipeSHA256": "DIGEST"
    }
  }],
  "last_run_uuid": "6e005968-cfd0-4f74-b290-361c25f5ac71"
}
`

func cloudCandidateFixture() (config.EC2Runtime, string) {
	raw := strings.ReplaceAll(officialCatalogFixture, "DIGEST", strings.Repeat("a", 64))
	digest := sha256.Sum256([]byte(raw))
	settings := config.EC2Runtime{AccountID: "111122223333", Region: "us-west-2", OfficialImage: &release.OfficialEC2Image{
		AMIID: "ami-0123456789abcdef0", PublisherAccountID: "222233334444",
		RootDeviceName: "/dev/sda1", RootSnapshotID: "snap-0123456789abcdef0",
		SourceAMI: "ami-0123456789abcdef1", SourceRegion: "us-east-2",
		SourceCommit: "aabbccddeeff00112233445566778899aabbccdd",
		ReleaseID:    "v1.4.2-rc.1", GuestRelease: "v1.4.2-rc.1", Architecture: "amd64",
		MinimumDiskGiB: 32, CatalogSHA256: hex.EncodeToString(digest[:]),
	}}
	return settings, raw
}

// TestCloudOfficialCatalog requires explicit selection and the exact release-supplied catalog bytes.
func TestCloudOfficialCatalog(t *testing.T) {
	settings, raw := cloudCandidateFixture()
	p := cloudEC2{settings: settings}
	opts := app.PlatformImportOptions{Official: true}
	image, err := p.readCandidate(raw, "v1.4.2-rc.1", "", opts)
	if err != nil || image.EC2 == nil || image.EC2.PublisherAccountID != settings.OfficialImage.PublisherAccountID ||
		image.EC2.SourceRegion != "us-east-2" || !image.EC2.UsesSingleBoot() {
		t.Fatalf("readCandidate(official) = %+v, %v, want pinned publisher, source region and single-boot mode", image.EC2, err)
	}
	for _, tc := range []struct {
		name string
		opts app.PlatformImportOptions
		raw  string
		want error
	}{
		{name: "unselected", raw: raw, want: release.ErrCatalogConflict},
		{name: "family", raw: raw, opts: app.PlatformImportOptions{Official: true, AMIFamily: "public"}, want: release.ErrCatalogConflict},
		{name: "modified bytes", raw: strings.TrimSpace(raw), opts: opts, want: release.ErrInvalidArtifact},
		{name: "wrong region", raw: strings.ReplaceAll(raw, "us-west-2", "us-west-1"), opts: opts, want: release.ErrCatalogConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := p.readCandidate(tc.raw, "v1.4.2-rc.1", "", tc.opts); !errors.Is(err, tc.want) {
				t.Fatalf("readCandidate(%s) = %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}

// TestCloudOfficialCatalogChecksLineageSeparately rejects valid catalog bytes paired with the wrong descriptor.
func TestCloudOfficialCatalogChecksLineageSeparately(t *testing.T) {
	settings, raw := cloudCandidateFixture()
	settings.OfficialImage.SourceRegion = "us-west-1"
	p := cloudEC2{settings: settings}
	if _, err := p.readCandidate(raw, "v1.4.2-rc.1", "", app.PlatformImportOptions{Official: true}); !errors.Is(err, release.ErrInvalidArtifact) {
		t.Fatalf("readCandidate(wrong source region) = %v, want invalid artifact", err)
	}
}

// TestOfficialEC2Configuration keeps publisher authority separate from the customer account.
func TestOfficialEC2Configuration(t *testing.T) {
	settings, _ := cloudCandidateFixture()
	cfg := config.Config{InstallationDir: t.TempDir(), ManagementOrigin: "https://shiftbox.example.test", DataProfile: "postgres",
		PostgresURI: "postgresql://postgres.example.test/shiftbox?sslmode=verify-full", ExternalAuthIntervalSeconds: 14400,
		Runtime: &config.ControllerRuntime{Provider: "ec2", EC2: &settings}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Config.Validate(valid official descriptor) = %v, want success", err)
	}
	settings.OfficialImage.PublisherAccountID = settings.AccountID
	if err := cfg.Validate(); err == nil {
		t.Fatal("Config.Validate(deployment account used as publisher) accepted identical accounts")
	}
	settings.OfficialImage = nil
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Config.Validate(legacy private image) = %v, want success", err)
	}
}
