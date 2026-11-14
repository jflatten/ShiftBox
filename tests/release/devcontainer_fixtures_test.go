package release_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDevContainerReleaseFixtures checks glibc amd64 builds, Feature and
// hook ordering, retry after a required hook failure, and named-volume data
// retention across a container rebuild.
func TestDevContainerReleaseFixtures(t *testing.T) {
	cli := os.Getenv("SHIFTBOX_TEST_DEVCONTAINER_CLI")
	if cli == "" {
		t.Skip("set SHIFTBOX_TEST_DEVCONTAINER_CLI to the pinned Dev Container CLI")
	}
	if !filepath.IsAbs(cli) {
		t.Fatal("Dev Container CLI path must be absolute")
	}
	run := func(t *testing.T, ctx context.Context, executable string, args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, executable, args...).Output()
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				t.Logf("%s", exit.Stderr)
			}
			t.Fatalf("%s %s failed: %v", filepath.Base(executable), args[0], err)
		}
		return strings.TrimSpace(string(out))
	}
	if got := run(t, t.Context(), cli, "--version"); got != "0.90.0" {
		t.Fatalf("Dev Container CLI version = %q, want 0.90.0", got)
	}

	for _, distro := range []string{"debian", "ubuntu"} {
		t.Run(distro, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			root := t.TempDir()
			if err := os.CopyFS(root, os.DirFS(filepath.Join("fixtures", distro))); err != nil {
				t.Fatal(err)
			}
			// The volume name is shared by both builds of this disposable
			// workspace but unique across test runs.
			t.Setenv("SHIFTBOX_FIXTURE_VOLUME", "shiftbox-release-"+distro+"-"+filepath.Base(filepath.Dir(root)))
			volume := os.Getenv("SHIFTBOX_FIXTURE_VOLUME")
			label := "shiftbox.release-fixture=" + volume
			args := []string{"--workspace-folder", root, "--id-label", label}
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
				defer stop()
				ids := strings.Fields(run(t, cleanup, "docker", "ps", "-aq", "--filter", "label="+label))
				if len(ids) > 0 {
					run(t, cleanup, "docker", append([]string{"rm", "--force"}, ids...)...)
				}
				if _, err := exec.CommandContext(cleanup, "docker", "volume", "inspect", volume).Output(); err == nil {
					run(t, cleanup, "docker", "volume", "rm", volume)
				}
			})
			check := func(command, want string) {
				t.Helper()
				actual := run(t, ctx, cli, append([]string{"exec"}, append(args, "sh", "-ec", command)...)...)
				if actual != want {
					t.Fatalf("%s evidence = %q, want %q", distro, actual, want)
				}
			}
			up := func(options ...string) string {
				t.Helper()
				result := run(t, ctx, cli, append([]string{"up", "--skip-post-create"}, append(args, options...)...)...)
				var response struct {
					ContainerID string `json:"containerId"`
				}
				if err := json.Unmarshal([]byte(result), &response); err != nil || response.ContainerID == "" {
					t.Fatal("Dev Container CLI did not identify its container")
				}
				return response.ContainerID
			}

			first := up()
			commands := append([]string{"run-user-commands", "--skip-non-blocking-commands"}, args...)
			if err := os.WriteFile(filepath.Join(root, "fail-hook"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.CommandContext(ctx, cli, commands...).Output()
			var failed struct {
				Outcome string `json:"outcome"`
			}
			if err == nil || json.Unmarshal(out, &failed) != nil || failed.Outcome != "error" {
				t.Fatal("required hook failure did not stop the fixture before waitFor")
			}
			check("test ! -e ready && test ! -e later && uname -m", "x86_64")
			check("printf retained > /var/lib/shiftbox-fixture/value", "")
			if err := os.Remove(filepath.Join(root, "fail-hook")); err != nil {
				t.Fatal(err)
			}

			if second := up("--remove-existing-container"); first == second {
				t.Fatal("fixture retained the original container across rebuild")
			}
			run(t, ctx, cli, commands...)
			check("test -e ready && test ! -e later && cat /var/lib/shiftbox-fixture/value", "retained")
			run(t, ctx, cli, append([]string{"run-user-commands", "--skip-post-attach"}, args...)...)
			check("cat later", "later")
		})
	}
}
