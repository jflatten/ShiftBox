package release_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPackerBuildPlan checks the pinned UTM cloud recipe with the real plugin
// and ensures its build seed uses a temporary SSH key.
func TestPackerBuildPlan(t *testing.T) {
	recipe := filepath.Join("..", "..", "platform", "packer", "shiftbox.pkr.hcl")
	raw, err := os.ReadFile(recipe)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"utm-cloud", "required_version", "sha256:d99d1abe3284e568161b3b7dabfbd6cf67956a7f4274b13842f10aa9c7807a2c", "release-20260716",
		"ssh_private_key_file", "lock_passwd: true", "ssh_pwauth: false", "shiftbox-start-agent"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("Packer recipe missing %q", want)
		}
	}
	if strings.Contains(string(raw), "ssh_password ") {
		t.Error("Packer recipe uses password-based SSH")
	}
	packer := os.Getenv("SHIFTBOX_TEST_PACKER_BINARY")
	if packer == "" {
		return
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "build-key")
	if out, err := exec.CommandContext(t.Context(), "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("create temporary fixture key: %v\n%s", err, out)
	}
	public, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"validate", "-var", "build_public_key=" + strings.TrimSpace(string(public)), "-var", "build_private_key=" + key,
		"-var", "agent_binary=" + key, "-var", "output_directory=" + filepath.Join(dir, "export"),
		"-var", "build_name=shiftbox-packer-fixture", recipe}
	if out, err := exec.CommandContext(t.Context(), packer, args...).CombinedOutput(); err != nil {
		t.Fatalf("packer validate: %v\n%s", err, out)
	}
}

// TestPackerGuestToolchain ensures the image recipe installs pinned tools
// and verifies the running Docker service before sealing the artifact.
func TestPackerGuestToolchain(t *testing.T) {
	root := filepath.Join("..", "..", "platform")
	for file, expected := range map[string][]string{
		"packer/shiftbox.pkr.hcl": {"install-toolchain.sh", "sudo /bin/sh /tmp/shiftbox-install-toolchain"},
		"packer/install-toolchain.sh": {"5:29.9.3-1~ubuntu.24.04~noble", "2.4.2-4~ubuntu.24.04~noble",
			"0.39.0-1~ubuntu.24.04~noble", "v24.22.3", "0.90.0", "sha256sum --check",
			"docker version", "docker buildx version", "devcontainer --version", "git --version"},
		"packer/seal-image.sh": {"/tmp/shiftbox-install-toolchain"},
	} {
		path := filepath.Join(root, file)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Error(err)
			continue
		}
		for _, marker := range expected {
			if !strings.Contains(string(raw), marker) {
				t.Errorf("%s missing pinned install or validation marker %q", path, marker)
			}
		}
	}
}

// TestPackerBrowserIDE requires a checksum-pinned IDE, preserved notices,
// and a sealed export whose manifest identifies the packaged component.
func TestPackerBrowserIDE(t *testing.T) {
	root := filepath.Join("..", "..", "platform", "packer")
	for file, expected := range map[string][]string{
		"code-server.env":      {"CODE_SERVER_VERSION=4.145.2", "2f6db6c3e8b733dd6b3aeee83532e148b2bae2e392353ba85452d03572348962"},
		"install-toolchain.sh": {"code-server-$CODE_SERVER_VERSION-linux-amd64.tar.gz", "ThirdPartyNotices", "code-server --version"},
		"shiftbox.pkr.hcl":     {"code-server.env", "/tmp/shiftbox-code-server.env"},
		"seal-image.sh":        {"/tmp/shiftbox-code-server.env"},
		"build-image.sh":       {"go run ./platform/packer/manifest", "manifest.json", "CODE_SERVER_SHA256"},
	} {
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Error(err)
			continue
		}
		for _, marker := range expected {
			if !strings.Contains(string(raw), marker) {
				t.Errorf("%s missing browser IDE build marker %q", file, marker)
			}
		}
	}
}

// TestPackerRepairImage checks that Packer preloads an independent Ubuntu
// repair container and verifies the packaged IDE without network access.
func TestPackerRepairImage(t *testing.T) {
	path := filepath.Join("..", "..", "platform", "packer", "install-toolchain.sh")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"ubuntu@sha256:77eb101c607d6e3d4f5ad0c8a02c2136c689b98aba1f359e6502ba517e5c57ec",
		"shiftbox/repair:ubuntu-24.04", "COPY . /opt/shiftbox-code-server/", "--pull=never --network=none"} {
		if !strings.Contains(string(raw), marker) {
			t.Errorf("%s missing repair image build or verification marker %q", path, marker)
		}
	}
}
