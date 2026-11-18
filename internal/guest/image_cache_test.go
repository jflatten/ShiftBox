package guest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestImageCacheHostsKeepsRestrictedPullsOnTheController checks the
// containerd terminal endpoint and verifies that the generated hosts file
// refers to the current guest client certificate, key, and installation CA.
func TestImageCacheHostsKeepsRestrictedPullsOnTheController(t *testing.T) {
	const endpoint = "https://controller.internal.test:9443"
	const ca = "/var/lib/shiftbox/trust.pem"
	const oldCert = "/var/lib/shiftbox/first-cert.pem"
	const oldKey = "/var/lib/shiftbox/first-key.pem"
	const newCert = "/var/lib/shiftbox/renewed-cert.pem"
	const newKey = "/var/lib/shiftbox/renewed-key.pem"
	for _, identity := range []struct{ cert, key string }{{oldCert, oldKey}, {newCert, newKey}} {
		content, err := imageCacheHosts(endpoint, false, ca, identity.cert, identity.key)
		if err != nil {
			t.Fatalf("imageCacheHosts(%q, false, %q, %q) = %v, want mirror-only config", endpoint, identity.cert, identity.key, err)
		}
		toml := string(content)
		for _, want := range []string{`server = "` + endpoint + `"`, `"pull"`, `"resolve"`, ca, identity.cert, identity.key} {
			if !strings.Contains(toml, want) {
				t.Errorf("mirror-only hosts.toml = %q, want %q", toml, want)
			}
		}
		if strings.Contains(toml, "skip_verify = true") || strings.Contains(strings.ReplaceAll(toml, endpoint, ""), "https://") {
			t.Errorf("mirror-only hosts.toml = %q, want verified controller TLS and no other endpoint", toml)
		}
	}
	if _, err := imageCacheHosts("https://controller.internal.test:9443/internal/v1/guest/poll", false, ca, newCert, newKey); err == nil {
		t.Error("imageCacheHosts(poll endpoint) succeeded, want rejection of non-origin endpoint")
	}
}

// TestPrepareImagePreservesProjectReferences verifies that metadata needed
// by the Dev Container CLI comes from a Docker pull of the original image,
// while Dockerfile builds keep their original input and builder path.
func TestPrepareImagePreservesProjectReferences(t *testing.T) {
	const image = "mcr.microsoft.com/devcontainers/base:ubuntu"
	const dockerfile = "FROM " + image + "\nRUN true\n"
	for _, tc := range []struct {
		name          string
		configuration string
		pullOriginal  bool
	}{
		{"prebuilt", `{"image":"` + image + `"}`, true},
		{"dockerfile", `{"build":{"dockerfile":"Dockerfile"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			config := filepath.Join(dir, "devcontainer.json")
			project := "// original JSONC\n" + tc.configuration + "\n"
			if err := os.WriteFile(config, []byte(project), 0600); err != nil {
				t.Fatal(err)
			}
			dockerPath := filepath.Join(dir, "Dockerfile")
			if !tc.pullOriginal {
				if err := os.WriteFile(dockerPath, []byte(dockerfile), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var commands [][]string
			d := devContainer{workspace: dir, config: "devcontainer.json", run: func(_ context.Context, path string, args ...string) ([]byte, error) {
				commands = append(commands, append([]string{path}, args...))
				switch {
				case path == devContainerCLI && len(args) > 0 && args[0] == "read-configuration":
					return []byte(`{"configuration":` + tc.configuration + `}`), nil
				case path == "/usr/bin/docker" && slices.Equal(args, []string{"pull", image}):
					return nil, nil
				default:
					return nil, errors.New("unexpected guest image preparation command")
				}
			}}
			if err := d.prepareImage(t.Context()); err != nil {
				t.Fatalf("prepareImage(%s) = %v, want success (commands %q)", tc.name, err, commands)
			}
			wantCommands := 1
			if tc.pullOriginal {
				wantCommands = 2
			}
			if len(commands) != wantCommands || !slices.Contains(commands[0], config) ||
				tc.pullOriginal && !slices.Equal(commands[1], []string{"/usr/bin/docker", "pull", image}) {
				t.Errorf("prepareImage(%s) commands = %q, want project configuration read and pull original=%t", tc.name, commands, tc.pullOriginal)
			}
			if got, err := os.ReadFile(config); err != nil || string(got) != project {
				t.Errorf("project configuration after prepareImage(%s) = %q, %v, want %q", tc.name, got, err, project)
			}
			if !tc.pullOriginal {
				if got, err := os.ReadFile(dockerPath); err != nil || string(got) != dockerfile {
					t.Errorf("Dockerfile after prepareImage(%s) = %q, %v, want %q", tc.name, got, err, dockerfile)
				}
			}
		})
	}
}

// TestOnlyVerifiedOpenEgressAllowsDirectFallback prevents a restricted or
// unknown workspace policy from inheriting the registry's parent endpoint.
func TestOnlyVerifiedOpenEgressAllowsDirectFallback(t *testing.T) {
	for _, fixture := range []struct {
		policy string
		want   string
	}{
		{`{"mode":"shared"}`, "mirror-and-direct"},
		{`{"mode":"no_network"}`, "mirror"},
		{`{"mode":"allowlist"}`, "mirror"},
		{`{"mode":"future"}`, "mirror"},
	} {
		got := ImageCacheMode(fixture.policy, true)
		if got != fixture.want {
			t.Errorf("ImageCacheMode(%s, true) = %q, want %q", fixture.policy, got, fixture.want)
		}
	}
}

// TestLiteralImagePreparation keeps simple project pulls on Docker's mirror and
// delegates substituted references and JSONC to the reference CLI.
func TestLiteralImagePreparation(t *testing.T) {
	const image = "mcr.microsoft.com/devcontainers/base:ubuntu-24.04"
	for _, tc := range []struct {
		name, config string
		resolved     bool
	}{
		{"literal", `{"remoteUser":"vscode","image":"` + image + `"}`, false},
		{"variable", `{"image":"${localEnv:BASE}"}`, true},
		{"jsonc", `{/*metadata*/"image":"` + image + `"}`, true},
		{"mixed case", `{"Image":"ignored","image":"` + image + `"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "devcontainer.json"), []byte(tc.config), 0600); err != nil {
				t.Fatal(err)
			}
			var calls []string
			d := devContainer{workspace: dir, config: "devcontainer.json", run: func(_ context.Context, cmd string, args ...string) ([]byte, error) {
				calls = append(calls, args[0])
				if cmd == devContainerCLI {
					return []byte(`{"configuration":{"image":"` + image + `"}}`), nil
				}
				if cmd != dockerExecutable || !slices.Equal(args, []string{"pull", image}) {
					t.Fatalf("unexpected pull %s %q", cmd, args)
				}
				return nil, nil
			}}
			if err := d.prepareImage(t.Context()); err != nil {
				t.Fatal(err)
			}
			want := []string{"pull"}
			if tc.resolved {
				want = []string{"read-configuration", "pull"}
			}
			if !slices.Equal(calls, want) {
				t.Errorf("prepareImage(%s) commands = %v, want %v", tc.name, calls, want)
			}
		})
	}
}
