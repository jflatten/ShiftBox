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
