package guest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	devContainerCLI       = "/usr/local/bin/devcontainer"
	devContainerOutputMax = 1 << 20
	developmentLabel      = "shiftbox.container=development"
	credentialDirectory   = "/run/shiftbox-credentials"
)

var dockerContainerID = regexp.MustCompile(`^[0-9a-f]{64}$`)

type devContainer struct {
	workspace   string
	config      string
	repository  *repositoryPreparation
	credentials bool
	run         func(context.Context, string, ...string) ([]byte, error)
}

// containerConnection records successful execution through the declared waitFor
// gate. The controller still needs evidence from its browser IDE proxy.
type containerConnection struct {
	ID      string
	WaitFor string
}

type devContainerResult struct {
	Outcome string `json:"outcome"`
	ID      string `json:"containerId"`
	Merged  *struct {
		WaitFor string `json:"waitFor"`
	} `json:"mergedConfiguration"`
}

func (d devContainer) ensure(ctx context.Context) (containerConnection, error) {
	// Let the reference CLI read JSONC, resolve Features and image metadata,
	// and execute lifecycle commands. The guest VM is the isolation boundary.
	result, err := d.invoke(ctx, "up", "--skip-non-blocking-commands", "--skip-post-attach",
		"--include-merged-configuration")
	if err != nil {
		return containerConnection{}, err
	}
	if !dockerContainerID.MatchString(result.ID) || result.Merged == nil {
		return containerConnection{}, errors.New("Dev Container CLI did not return connection evidence")
	}
	waitFor := result.Merged.WaitFor
	if waitFor == "" {
		waitFor = "updateContentCommand"
	}
	if !validWaitFor(waitFor) {
		return containerConnection{}, errors.New("invalid Dev Container waitFor stage")
	}
	return containerConnection{ID: result.ID, WaitFor: waitFor}, nil
}

func (d devContainer) finishSetup(ctx context.Context, connection containerConnection) error {
	if !dockerContainerID.MatchString(connection.ID) || !validWaitFor(connection.WaitFor) {
		return errors.New("invalid Dev Container connection evidence")
	}
	// The CLI tracks completed creation and start hooks in the container.
	// Attach hooks belong to the IDE attach path.
	_, err := d.invoke(ctx, "run-user-commands", "--container-id", connection.ID, "--skip-post-attach")
	return err
}

func (d devContainer) invoke(ctx context.Context, stage string, options ...string) (devContainerResult, error) {
	if !filepath.IsAbs(d.workspace) {
		return devContainerResult{}, errors.New("invalid Dev Container workspace path")
	}
	// Use the same label for creation and continuation so the CLI does not
	// append a second copy of project hooks to the saved container metadata.
	args := []string{stage, "--workspace-folder", d.workspace, "--docker-path", "/usr/bin/docker",
		"--docker-compose-path", "/bin/false", "--log-format", "json", "--id-label", developmentLabel}
	if stage == "up" && d.credentials {
		// Mount the directory so an agent restart can replace its listener.
		// The command file is a runtime copy, separate from the agent binary.
		args = append(args,
			"--mount", "type=bind,source="+credentialDirectory+",target="+credentialDirectory,
			"--mount", "type=bind,source="+credentialDirectory+"/shiftbox,target=/usr/local/bin/shiftbox")
	}
	if d.config != "" {
		if !validDevContainerPath(d.config) {
			return devContainerResult{}, errors.New("invalid Dev Container configuration path")
		}
		args = append(args, "--config", filepath.Join(d.workspace, d.config))
	}
	run := d.run
	if run == nil {
		run = runDevContainerCLI
	}
	output, err := run(ctx, devContainerCLI, append(args, options...)...)
	if err != nil {
		if ctx.Err() != nil {
			return devContainerResult{}, ctx.Err()
		}
		return devContainerResult{}, errors.New("Dev Container CLI " + stage + " failed")
	}
	var result devContainerResult
	if len(output) > devContainerOutputMax || json.Unmarshal(output, &result) != nil || result.Outcome != "success" {
		return devContainerResult{}, errors.New("Dev Container CLI " + stage + " did not report success")
	}
	return result, nil
}

func validDevContainerPath(path string) bool {
	return path != "." && fs.ValidPath(path) && !strings.ContainsAny(path, "\\\x00")
}

func validWaitFor(stage string) bool {
	switch stage {
	case "initializeCommand", "onCreateCommand", "updateContentCommand", "postCreateCommand", "postStartCommand":
		return true
	default:
		return false
	}
}

func runDevContainerCLI(ctx context.Context, executable string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	var stdout devContainerOutput
	cmd.Stdout = &stdout
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.buffer.Bytes(), nil
}

// The structured stdout response can contain project metadata. Bound it and
// leave stderr out of operation evidence.
type devContainerOutput struct {
	buffer bytes.Buffer
}

func (o *devContainerOutput) Write(data []byte) (int, error) {
	if len(data) > devContainerOutputMax-o.buffer.Len() {
		return 0, errors.New("Dev Container CLI response exceeds the output limit")
	}
	return o.buffer.Write(data)
}
