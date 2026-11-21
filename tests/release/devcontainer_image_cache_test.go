package release_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

const cliBundle = "dist/spec-node/devContainersSpecCLI.js"

var fixtureImageID = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type cachedDevContainerResult struct {
	Outcome           string `json:"outcome"`
	ContainerID       string `json:"containerId"`
	RemoteUser        string `json:"remoteUser"`
	Workspace         string `json:"remoteWorkspaceFolder"`
	DisallowedFeature string `json:"disallowedFeatureId"`
	Image             struct {
		ID           string `json:"builtImageID"`
		ConfigSHA256 string `json:"configSHA256"`
		Hit          bool   `json:"imageCacheHit"`
	} `json:"shiftboxImage"`
	Merged map[string]json.RawMessage `json:"mergedConfiguration"`
}

// TestDevContainerBuiltImageCacheProof exercises the pinned Linux guest CLI's
// image resolver against ordinary builds and digest hits. It checks original
// JSONC bytes, metadata parity, policy and initializer ordering, retained-home
// recreation, the waitFor gate, and a real code-server workbench on both paths
// that can produce a project image. Use a disposable amd64 ShiftBox guest.
func TestDevContainerBuiltImageCacheProof(t *testing.T) {
	packageDir := os.Getenv("SHIFTBOX_TEST_DEVCONTAINER_PACKAGE")
	if packageDir == "" {
		t.Skip("set SHIFTBOX_TEST_DEVCONTAINER_PACKAGE to the pinned guest CLI package")
	}
	if !filepath.IsAbs(packageDir) {
		t.Fatal("SHIFTBOX_TEST_DEVCONTAINER_PACKAGE must be absolute")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 9*time.Minute)
	defer cancel()
	cli := prepareFixtureCacheCLI(t, ctx, packageDir)
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Fatal("run the resolver proof in a disposable Linux amd64 guest")
	}
	bundle := os.Getenv("SHIFTBOX_TEST_CODE_SERVER_PACKAGE")
	if bundle == "" {
		bundle = "/usr/local/lib/shiftbox/code-server"
	}
	if !filepath.IsAbs(bundle) {
		t.Fatal("SHIFTBOX_TEST_CODE_SERVER_PACKAGE must be absolute")
	}
	out := fixtureCommand(t, ctx, filepath.Join(bundle, "bin", "code-server"), "--config", "/dev/null", "--version", "--json")
	var version struct {
		CodeServer string `json:"codeServer"`
	}
	if err := json.Unmarshal([]byte(out), &version); err != nil || version.CodeServer != "4.145.2" {
		t.Fatalf("code-server --version --json = %q (%v), want codeServer version 4.145.2", out, err)
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	if got := fixtureCommand(t, ctx, docker, "version", "--format", "{{.Server.Version}}/{{.Client.Version}}"); got != "29.9.3/29.9.3" {
		t.Fatalf("Docker client/server versions = %q, want 29.9.3/29.9.3", got)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name     string
		distro   string
		adaptUID bool
	}{
		{name: "image-with-features", distro: "debian"},
		{name: "dockerfile", distro: "ubuntu", adaptUID: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			proveDevContainerCacheFixture(t, ctx, cli, docker, bundle, account.Username, fixture.distro, fixture.adaptUID)
		})
	}
}

func proveDevContainerCacheFixture(t *testing.T, ctx context.Context, cli, docker, bundle, username, distro string, adaptUID bool) {
	t.Helper()
	root := t.TempDir()
	// The remote workspace user needs to traverse the bind-mounted fixture.
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", root)
	manifest := filepath.Join(root, "devcontainercli-"+username, "control-manifest.json")
	if err := os.MkdirAll(filepath.Dir(manifest), 0700); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, manifest, `{"disallowedFeatures":[]}`, 0600)
	fixtureUniqueID := filepath.Base(filepath.Dir(root)) + "-" + distro
	label := "shiftbox.cache-proof=" + fixtureUniqueID
	volume := "shiftbox-cache-proof-" + fixtureUniqueID
	t.Setenv("SHIFTBOX_FIXTURE_VOLUME", volume)
	t.Setenv("SHIFTBOX_CACHE_PROOF_REAL_DOCKER", docker)
	trace := filepath.Join(root, "build-trace")
	t.Setenv("SHIFTBOX_CACHE_PROOF_TRACE", trace)
	wrapper := filepath.Join(root, "docker")
	fixtureFile(t, wrapper, fixtureDockerTrace, 0700)
	t.Cleanup(func() { cleanupFixtureCacheDocker(t, docker, volume, label, trace) })

	base, err := os.ReadFile(filepath.Join("fixtures", distro, ".devcontainer", "devcontainer.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The initializer edits only a JSONC comment after the CLI has read and
	// parsed the configuration. A second read or a normalized digest is wrong.
	config := strings.Replace(string(base), "{", `{
  // Raw whitespace and comments are part of the match.
  "initializeCommand": "printf 'initializing\n' >> initialized && printf '\n// initializer edit\n' >> .devcontainer/devcontainer.json",
  "customizations": { "vscode": { "settings": { "editor.tabSize": 3 } } },
  "remoteEnv": { "SHIFTBOX_CACHE_PROOF_WORKSPACE": "${localWorkspaceFolderBasename}" },`, 1)
	if adaptUID {
		config = strings.Replace(config, "{", `{"remoteUser":"vscode",`, 1)
	}
	configDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(config)))
	newWorkspace := func(name string) string {
		t.Helper()
		path := filepath.Join(root, name)
		copyDevContainerReleaseFixture(t, path, distro)
		if err := os.Chmod(path, 0777); err != nil {
			t.Fatal(err)
		}
		fixtureFile(t, filepath.Join(path, ".devcontainer", "devcontainer.json"), config, 0644)
		fixtureFile(t, filepath.Join(path, ".devcontainer", "local-feature", "devcontainer-feature.json"), `{
  "id":"local-feature", "version":"1.0.0", "name":"ShiftBox release fixture marker",
  "onCreateCommand":"printf 'feature-hook\n' >> feature-hook",
  "customizations":{"vscode":{"settings":{"editor.insertSpaces":true}}}
}`, 0644)
		if adaptUID {
			// The Dockerfile variant exercises explicit selection while the
			// image-plus-Features variant uses the CLI's default search.
			fixtureFile(t, filepath.Join(path, ".devcontainer.json"), `{}`, 0644)
		}
		return path
	}
	cliArgs := func(workspace, phase string) []string {
		args := []string{cli, phase, "--workspace-folder", workspace,
			"--docker-path", wrapper, "--docker-compose-path", "/bin/false", "--id-label", label}
		if adaptUID {
			args = append(args, "--config", filepath.Join(workspace, ".devcontainer", "devcontainer.json"))
		}
		return args
	}
	up := func(workspace, uidMode string, options ...string) (cachedDevContainerResult, error) {
		t.Helper()
		args := append(cliArgs(workspace, "up"), "--log-format", "json", "--skip-post-create", "--skip-post-attach", "--include-merged-configuration", "--update-remote-user-uid-default", uidMode)
		out, err := fixtureCommandOutput(ctx, "node", append(args, options...)...)
		var result cachedDevContainerResult
		if decodeErr := json.Unmarshal(out, &result); decodeErr != nil {
			t.Fatalf("devcontainer up (%s) response = %q, %v, want an outcome: %v", filepath.Base(workspace), out, decodeErr, err)
		}
		return result, err
	}
	upOK := func(workspace, uidMode string, options ...string) cachedDevContainerResult {
		t.Helper()
		result, err := up(workspace, uidMode, options...)
		if err != nil || result.Outcome != "success" || result.ContainerID == "" {
			t.Fatalf("devcontainer up (%s) = %+v, %v, want success and a container ID", filepath.Base(workspace), result, err)
		}
		return result
	}
	checkInitializer := func(workspace string, runs int) {
		t.Helper()
		initialized, initErr := os.ReadFile(filepath.Join(workspace, "initialized"))
		file, readErr := os.ReadFile(filepath.Join(workspace, ".devcontainer", "devcontainer.json"))
		if initErr != nil || string(initialized) != strings.Repeat("initializing\n", runs) || readErr != nil || !strings.HasSuffix(string(file), "\n// initializer edit\n") {
			t.Fatalf("initializeCommand (%s) = %q, %v, config read %v, want %d runs and a file edit", filepath.Base(workspace), initialized, initErr, readErr, runs)
		}
	}

	coldWorkspace := newWorkspace("cold")
	uidMode := "off"
	if adaptUID {
		uidMode = "on"
	}
	cold := upOK(coldWorkspace, uidMode)
	checkInitializer(coldWorkspace, 1)
	if !fixtureImageID.MatchString(cold.Image.ID) || cold.Image.ConfigSHA256 != configDigest || cold.Image.Hit {
		t.Fatalf("cold image evidence = %+v, want built image with original config digest %s", cold.Image, configDigest)
	}
	if got := fixtureCommand(t, ctx, docker, "image", "inspect", "--format", "{{.Id}}", cold.Image.ID); got != cold.Image.ID {
		t.Fatalf("Docker image ID = %q, want %s", got, cold.Image.ID)
	}
	coldRunningImage := fixtureCommand(t, ctx, docker, "container", "inspect", "--format", "{{.Image}}", cold.ContainerID)
	if adaptUID == (coldRunningImage == cold.Image.ID) {
		t.Fatalf("running image ID = %s, captured ID = %s, expected UID adaptation = %t", coldRunningImage, cold.Image.ID, adaptUID)
	}
	traceBeforeHit, err := os.ReadFile(trace)
	if err != nil || !strings.Contains(string(traceBeforeHit), "project\t") || adaptUID && !strings.Contains(string(traceBeforeHit), "uid\t") {
		t.Fatalf("cold build trace = %q, %v, want a project build and UID adaptation %t", traceBeforeHit, err, adaptUID)
	}
	fixtureCommand(t, ctx, docker, "rm", "--force", cold.ContainerID)

	t.Setenv("SHIFTBOX_DEVCONTAINER_CACHE_IMAGE", cold.Image.ID)
	t.Setenv("SHIFTBOX_DEVCONTAINER_CACHE_CONFIG_SHA256", configDigest)
	t.Setenv("SHIFTBOX_CACHE_PROOF_FORBID_BUILD", "1")
	denied := newWorkspace("denied")
	fixtureFile(t, manifest, `{"disallowedFeatures":[{"featureIdPrefix":"./local-feature"}]}`, 0600)
	denial, err := up(denied, "off")
	if err == nil || denial.Outcome != "error" || denial.DisallowedFeature != "./local-feature" {
		t.Fatalf("cache hit with disallowed Feature = %+v, %v, want Feature denial", denial, err)
	}
	if _, err := os.Stat(filepath.Join(denied, "initialized")); !os.IsNotExist(err) {
		t.Fatalf("initializer marker after Feature denial = %v, want absent", err)
	}
	fixtureFile(t, manifest, `{"disallowedFeatures":[]}`, 0600)

	hitWorkspace := newWorkspace("hit")
	ide := filepath.Join(root, "ide")
	if err := os.Mkdir(ide, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ide, 0777); err != nil {
		t.Fatal(err)
	}
	ideMount := "type=bind,source=" + ide + ",target=/run/shiftbox-cache-proof"
	hit := upOK(hitWorkspace, "off", "--mount", ideMount)
	checkInitializer(hitWorkspace, 1)
	checkFixtureCacheHit(t, ctx, docker, cold, hit)
	home := fixtureCommand(t, ctx, docker, "exec", "--user", hit.RemoteUser, hit.ContainerID, "sh", "-ec", `getent passwd "$(id -u)" | cut -d: -f6`)
	if !filepath.IsAbs(home) || home == "/" {
		t.Fatalf("fixture developer home = %q, want an absolute non-root directory", home)
	}
	retained := filepath.Join(root, "home")
	if err := os.Mkdir(retained, 0700); err != nil {
		t.Fatal(err)
	}
	fixtureCommand(t, ctx, docker, "exec", "--user", hit.RemoteUser, "--env", "FIXTURE_HOME="+home, hit.ContainerID, "sh", "-ec", `printf 'retained-home' > "$FIXTURE_HOME/shiftbox-proof"`)
	fixtureCommand(t, ctx, docker, "cp", hit.ContainerID+":"+home+"/.", retained)
	owner := fixtureCommand(t, ctx, docker, "exec", "--user", hit.RemoteUser, hit.ContainerID, "sh", "-ec", `printf '%s:%s' "$(id -u)" "$(id -g)"`)
	localOwner := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	if owner != localOwner {
		// Docker can set the bind path owner when the test runner is non-root.
		// Release the changed ownership before the temporary directory is removed.
		changeOwner := func(ctx context.Context, owner string) {
			t.Helper()
			fixtureCommand(t, ctx, docker, "run", "--rm", "--pull=never", "--network=none", "--user", "root", "--entrypoint", "chown",
				"--mount", "type=bind,source="+retained+",target=/retained", cold.Image.ID, "-R", owner, "/retained")
		}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
			defer stop()
			changeOwner(cleanup, localOwner)
		})
		changeOwner(ctx, owner)
	}
	fixtureFile(t, filepath.Join(hitWorkspace, ".devcontainer", "devcontainer.json"), config, 0644)
	recreated := upOK(hitWorkspace, "off", "--remove-existing-container", "--mount", ideMount, "--mount", "type=bind,source="+retained+",target="+home)
	checkInitializer(hitWorkspace, 2)
	checkFixtureCacheHit(t, ctx, docker, cold, recreated)
	if recreated.ContainerID == hit.ContainerID {
		t.Fatal("retained-home setup reused the original container")
	}
	if got := fixtureCommand(t, ctx, docker, "exec", "--user", recreated.RemoteUser, recreated.ContainerID, "cat", home+"/shiftbox-proof"); got != "retained-home" {
		t.Fatalf("retained home marker = %q, want retained-home", got)
	}

	commands := append(cliArgs(hitWorkspace, "run-user-commands"), "--container-id", recreated.ContainerID, "--skip-post-attach")
	fixtureCommand(t, ctx, "node", append(commands, "--skip-non-blocking-commands")...)
	checkWorkspace := func(command, want string) {
		t.Helper()
		args := append(cliArgs(hitWorkspace, "exec"), "--container-id", recreated.ContainerID, "--", "sh", "-ec", command)
		if got := fixtureCommand(t, ctx, "node", args...); got != want {
			t.Fatalf("devcontainer exec (%s) = %q, want %q", command, got, want)
		}
	}
	checkWorkspace(`test -f ready && test ! -e later && printf '%s' "$SHIFTBOX_CACHE_PROOF_WORKSPACE"`, "hit")
	checkWorkspace("cat feature-hook", "feature-hook")
	checkFixtureCodeServer(t, ctx, docker, bundle, ide, recreated)
	fixtureCommand(t, ctx, "node", commands...)
	checkWorkspace("cat later", "later")
	traceAfterHit, err := os.ReadFile(trace)
	if err != nil || string(traceAfterHit) != string(traceBeforeHit) {
		t.Fatalf("digest hits changed build trace from %q to %q (%v)", traceBeforeHit, traceAfterHit, err)
	}
	fixtureCommand(t, ctx, docker, "rm", "--force", recreated.ContainerID)
	if adaptUID {
		// Projects can request UID adaptation even though ShiftBox normally
		// disables it. A reused image still needs the normal UID build, but it
		// must not build the Dockerfile or Features again.
		uidWorkspace := newWorkspace("uid-hit")
		uidHit := upOK(uidWorkspace, "on")
		if image := fixtureCommand(t, ctx, docker, "container", "inspect", "--format", "{{.Image}}", uidHit.ContainerID); image == cold.Image.ID || !uidHit.Image.Hit || uidHit.Image.ID != "" {
			t.Fatalf("UID-adapted hit used image %s and receipt %+v, want a derived image and a cache hit", image, uidHit.Image)
		}
		traceAfterUID, err := os.ReadFile(trace)
		if err != nil || !strings.HasPrefix(string(traceAfterUID), string(traceAfterHit)) || !strings.HasPrefix(strings.TrimPrefix(string(traceAfterUID), string(traceAfterHit)), "uid\t") {
			t.Fatalf("UID-adapted hit trace = %q, %v, want a UID build after %q", traceAfterUID, err, traceAfterHit)
		}
		fixtureCommand(t, ctx, docker, "rm", "--force", uidHit.ContainerID)
	}

	// A comment-only edit is a cache miss even though the parsed build input
	// is the same. Ordinary build and startup must remain available.
	t.Setenv("SHIFTBOX_CACHE_PROOF_FORBID_BUILD", "")
	changed := newWorkspace("changed")
	changedConfig := config + "\n// different origin bytes\n"
	fixtureFile(t, filepath.Join(changed, ".devcontainer", "devcontainer.json"), changedConfig, 0644)
	miss := upOK(changed, "off")
	if miss.Image.Hit || miss.Image.ConfigSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(changedConfig))) || !fixtureImageID.MatchString(miss.Image.ID) {
		t.Fatalf("different config bytes = %+v, want ordinary build with a new digest", miss.Image)
	}
	t.Logf("%s verified pre-UID capture, config SHA-256 %s, and no build on digest hits", distro, configDigest)
}

func checkFixtureCacheHit(t *testing.T, ctx context.Context, docker string, cold, hit cachedDevContainerResult) {
	t.Helper()
	if !hit.Image.Hit || hit.Image.ID != "" || hit.Image.ConfigSHA256 != cold.Image.ConfigSHA256 {
		t.Fatalf("digest hit evidence = %+v, want hit with original config digest and no new build", hit.Image)
	}
	if got := fixtureCommand(t, ctx, docker, "container", "inspect", "--format", "{{.Image}}", hit.ContainerID); got != cold.Image.ID {
		t.Fatalf("digest hit container image = %q, want %s", got, cold.Image.ID)
	}
	for _, key := range []string{"customizations", "remoteUser", "waitFor", "onCreateCommands", "postCreateCommands", "postStartCommands"} {
		if got, want := string(hit.Merged[key]), string(cold.Merged[key]); got != want {
			t.Errorf("digest hit metadata %s = %s, want %s", key, got, want)
		}
	}
	if string(hit.Merged["waitFor"]) != `"postCreateCommand"` || !strings.Contains(string(hit.Merged["customizations"]), `"editor.tabSize":3`) {
		t.Fatalf("digest hit merged metadata = %+v, want waitFor and VS Code settings", hit.Merged)
	}
}

func checkFixtureCodeServer(t *testing.T, ctx context.Context, docker, bundle, ide string, result cachedDevContainerResult) {
	t.Helper()
	fixtureCommand(t, ctx, docker, "cp", bundle, result.ContainerID+":/opt/shiftbox-code-server")
	fixtureCommand(t, ctx, docker, "exec", "--detach", "--user", result.RemoteUser, "--workdir", result.Workspace, result.ContainerID,
		"/opt/shiftbox-code-server/bin/code-server", "--config", "/dev/null", "--auth", "none", "--socket", "/run/shiftbox-cache-proof/ide.sock", "--socket-mode", "0666",
		"--disable-proxy", "--disable-update-check", "--disable-telemetry", "--ignore-last-opened", "--", result.Workspace)
	dialer := &net.Dialer{}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", filepath.Join(ide, "ide.sock"))
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	probe, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	for probe.Err() == nil {
		req, err := http.NewRequestWithContext(probe, http.MethodGet, "http://localhost/", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if closeErr := resp.Body.Close(); readErr == nil && closeErr == nil && resp.StatusCode == http.StatusOK && strings.Contains(string(body), "workbench") {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("code-server did not serve the browser workbench from the reused image before the deadline")
}

func fixtureFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func fixtureCommandOutput(ctx context.Context, executable string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	out, err := cmd.Output()
	if failure, ok := err.(*exec.ExitError); ok {
		return out, fmt.Errorf("%w\n%s", err, failure.Stderr)
	}
	return out, err
}

func fixtureCommand(t *testing.T, ctx context.Context, executable string, args ...string) string {
	t.Helper()
	out, err := fixtureCommandOutput(ctx, executable, args...)
	if err != nil {
		t.Fatalf("%s %v = %q, %v, want success", filepath.Base(executable), args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func cleanupFixtureCacheDocker(t *testing.T, docker, volume, label, trace string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	ids := strings.Fields(fixtureCommand(t, ctx, docker, "ps", "-aq", "--filter", "label="+label))
	for _, id := range ids {
		fixtureCommand(t, ctx, docker, "rm", "--force", id)
	}
	volumes := strings.Fields(fixtureCommand(t, ctx, docker, "volume", "ls", "--quiet", "--filter", "name="+volume))
	for _, name := range volumes {
		if name == volume {
			fixtureCommand(t, ctx, docker, "volume", "rm", volume)
		}
	}
	record, err := os.ReadFile(trace)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Errorf("read fixture build trace for cleanup: %v", err)
		return
	}
	tags := make(map[string]bool)
	for line := range strings.Lines(string(record)) {
		_, tag, _ := strings.Cut(strings.TrimSpace(line), "\t")
		if strings.HasPrefix(tag, "vsc-") {
			tags[tag] = true
		}
	}
	for tag := range tags {
		images := strings.Fields(fixtureCommand(t, ctx, docker, "images", "--quiet", tag))
		if len(images) > 0 {
			fixtureCommand(t, ctx, docker, "image", "rm", tag)
		}
	}
}

const fixtureDockerTrace = `#!/bin/sh
set -eu
if [ "$1" = pull ] && [ "${SHIFTBOX_CACHE_PROOF_FORBID_BUILD-}" = 1 ]; then
    echo 'unexpected Docker pull for an already local cache hit' >&2
    exit 1
fi
if [ "$1" = build ] || { [ "$1" = buildx ] && [ "${2-}" = build ]; }; then
    kind=project
    tag=
    previous=
    for argument do
        case "$previous:$argument" in
            -f:*/updateUID.Dockerfile-*) kind=uid ;;
            -t:*|--tag:*) tag=$argument ;;
        esac
        previous=$argument
    done
    printf '%s\t%s\n' "$kind" "$tag" >> "$SHIFTBOX_CACHE_PROOF_TRACE"
    if [ "$kind" = project ] && [ "${SHIFTBOX_CACHE_PROOF_FORBID_BUILD-}" = 1 ]; then
        echo 'unexpected project image build on cache hit' >&2
        exit 1
    fi
fi
exec "$SHIFTBOX_CACHE_PROOF_REAL_DOCKER" "$@"
`

// prepareFixtureCacheCLI uses the exact packaged extension. A proof begun
// from the published stock CLI can apply it on a disposable copy first.
func prepareFixtureCacheCLI(t *testing.T, ctx context.Context, packageDir string) string {
	t.Helper()
	bundle := filepath.Join(packageDir, cliBundle)
	original, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(original), "SHIFTBOX_DEVCONTAINER_CACHE_IMAGE") {
		root := t.TempDir()
		if err := os.CopyFS(root, os.DirFS(packageDir)); err != nil {
			t.Fatal(err)
		}
		bundle = filepath.Join(root, cliBundle)
		extension := os.Getenv("SHIFTBOX_TEST_DEVCONTAINER_EXTENSION")
		if extension == "" {
			extension = filepath.Join("..", "..", "platform", "packer", "extend-devcontainer-cli.cjs")
		}
		fixtureCommand(t, ctx, "node", extension, bundle)
	}
	fixtureCommand(t, ctx, "node", "--check", bundle)
	if got := fixtureCommand(t, ctx, "node", bundle, "--version"); got != "0.90.0" {
		t.Fatalf("Dev Container CLI version = %q, want 0.90.0", got)
	}
	return bundle
}
