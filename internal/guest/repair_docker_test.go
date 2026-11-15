//go:build utm_release

package guest

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const repairTestBase = "ubuntu@sha256:77eb101c607d6e3d4f5ad0c8a02c2136c689b98aba1f359e6502ba517e5c57ec"

// TestRepairDockerFailures exercises the trusted repair workbench against real
// Docker and Dev Container CLI failures at different project setup stages.
func TestRepairDockerFailures(t *testing.T) {
	image, cli := os.Getenv("SHIFTBOX_TEST_REPAIR_IMAGE"), os.Getenv("SHIFTBOX_TEST_DEVCONTAINER_CLI")
	if image == "" || cli == "" {
		t.Skip("set the local repair image digest and pinned Dev Container CLI path")
	}
	digest, ok := strings.CutPrefix(image, "sha256:")
	if !ok || !dockerContainerID.MatchString(digest) || !filepath.IsAbs(cli) {
		t.Fatal("use a local repair image SHA-256 ID and absolute Dev Container CLI path")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	if version, err := exec.CommandContext(t.Context(), cli, "--version").Output(); err != nil || strings.TrimSpace(string(version)) != "0.90.0" {
		t.Fatalf("Dev Container CLI version = %q (%v), want 0.90.0", version, err)
	}
	development := `"image":"` + repairTestBase + `","runArgs":["--platform=linux/amd64"]`
	for _, test := range []struct {
		name, configuration string
	}{
		{"configuration", "{"},
		{"build", `{"build":{"dockerfile":"Dockerfile","options":["--platform=linux/amd64"]}}`},
		{"hook", `{` + development + `,"waitFor":"postCreateCommand","postCreateCommand":"printf ran >> hook-tally; exit 19"}`},
		{"ide", `{` + development + `}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			testRepairDockerFailure(t, docker, cli, image, test.name, test.configuration)
		})
	}
}

func testRepairDockerFailure(t *testing.T, docker, cli, image, stage, configuration string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	dir := t.TempDir()
	project, sockets, empty := filepath.Join(dir, "project"), filepath.Join(dir, "sockets"), filepath.Join(dir, "empty-ide")
	for _, path := range []string{project, sockets, empty} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	imgFile := filepath.Join(dir, "repair-image")
	for path, data := range map[string]string{
		imgFile: image, filepath.Join(project, ".devcontainer.json"): configuration,
		filepath.Join(project, "retained.txt"): "work before setup failure",
		filepath.Join(project, "Dockerfile"):   "FROM " + repairTestBase + "\nRUN exit 19\n",
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", project}, args...)...).Output()
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	git("init", "--quiet")
	git("add", "--", "retained.txt")
	index := git("ls-files", "--stage")

	scope := "shiftbox.release-repair=" + uuid.NewString()
	runDocker := func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, docker, args...).Output()
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		out, err := runDocker(cleanup, "container", "ls", "-aq", "--filter", "label="+scope)
		if err != nil {
			t.Error(err)
			return
		}
		if ids := strings.Fields(string(out)); len(ids) != 0 {
			if _, err := runDocker(cleanup, append([]string{"container", "rm", "--force"}, ids...)...); err != nil {
				t.Error(err)
			}
		}
	})
	d := devContainer{workspace: project, config: ".devcontainer.json", codeServerSource: testCodeServerPackage(t),
		repairImageFile: imgFile, repairSocketDir: sockets}
	var cliFailure string
	d.run = func(ctx context.Context, executable string, args ...string) ([]byte, error) {
		args = slices.Clone(args)
		if executable == devContainerCLI {
			args[slices.Index(args, "--docker-path")+1] = docker
			out, err := exec.CommandContext(ctx, cli, slices.Insert(args, 1, "--id-label", scope)...).Output()
			if exit, ok := errors.AsType[*exec.ExitError](err); ok {
				cliFailure = string(exit.Stderr)
			}
			return out, err
		}
		switch {
		case slices.Equal(args[:2], []string{"container", "ls"}):
			args = append(args, "--filter", "label="+scope)
		case slices.Equal(args[:2], []string{"container", "run"}):
			args[slices.Index(args, "--name")+1] = "shiftbox-repair-" + strings.TrimPrefix(scope, "shiftbox.release-repair=")
			args = slices.Insert(args, 2, "--platform=linux/amd64", "--label", scope)
		case args[0] == "cp":
			// Inject the normal IDE installation failure before using the
			// independently built repair image.
			args[1] = empty + "/."
		}
		return runDocker(ctx, args...)
	}
	connection, err := d.ensure(ctx)
	if stage == "ide" {
		if err != nil {
			t.Fatalf("development container did not reach IDE startup: %v", err)
		}
		err = d.startIDE(ctx, connection.ID)
	}
	if err == nil || (stage == "ide" && !errors.Is(err, errBrowserIDE)) {
		t.Fatalf("%s fixture setup = %v, want the expected failure", stage, err)
	}
	if stage == "build" && !strings.Contains(cliFailure, "exit code: 19") {
		t.Fatal("Docker did not report the expected build-step exit code")
	}
	tally := filepath.Join(project, "hook-tally")
	before, tallyErr := os.ReadFile(tally)
	if stage == "hook" && (tallyErr != nil || string(before) != "ran") {
		t.Fatalf("required hook evidence = %q (%v), want one real hook execution before repair", before, tallyErr)
	}

	id, err := d.enterRepair(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := runDocker(ctx, "container", "inspect", "--format", "{{.HostConfig.NetworkMode}} {{.HostConfig.ReadonlyRootfs}}", id); err != nil || strings.TrimSpace(string(out)) != "none true" {
		t.Fatalf("repair runtime isolation = %q (%v), want no network and a read-only root", out, err)
	}
	const readWorkbench = `
const fs = require('node:fs');
const http = require('node:http');
const settings = JSON.parse(fs.readFileSync(process.env.HOME + '/data/User/settings.json'));
if (settings['task.allowAutomaticTasks'] !== 'off' || !settings['extensions.ignoreRecommendations']) process.exit(1);
if (fs.readFileSync('/project/retained.txt', 'utf8') !== 'work before setup failure') process.exit(1);
http.get({socketPath: '/run/shiftbox-repair/repair.sock', path: '/?folder=/project'}, (response) => {
  let body = '';
  response.on('data', (chunk) => { body += chunk; });
  response.on('end', () => {
    if (response.statusCode !== 200 || !body.includes('workbench')) process.exitCode = 1;
    else fs.writeFileSync('/project/retained.txt', 'saved in repair');
  });
}).on('error', () => { process.exitCode = 1; });
`
	if _, err := runDocker(ctx, "container", "exec", id, codeServerDir+"/lib/node", "-e", readWorkbench); err != nil {
		t.Fatalf("repair workbench did not serve the offline checkout: %v", err)
	}
	if err := d.exitRepair(ctx); err != nil {
		t.Fatal(err)
	}
	if remaining, err := d.repairContainer(ctx); err != nil || remaining != "" {
		t.Fatalf("repair container after exit = %q (%v), want removal", remaining, err)
	}
	if data, err := os.ReadFile(filepath.Join(project, "retained.txt")); err != nil || string(data) != "saved in repair" {
		t.Fatalf("retained work after repair = %q (%v), want edited contents", data, err)
	}
	if !bytes.Equal(git("ls-files", "--stage"), index) {
		t.Fatal("repair changed the retained Git index")
	}
	after, err := os.ReadFile(tally)
	if string(after) != string(before) || errors.Is(err, os.ErrNotExist) != errors.Is(tallyErr, os.ErrNotExist) {
		t.Fatalf("repair changed hook evidence from %q (%v) to %q (%v)", before, tallyErr, after, err)
	}
}
