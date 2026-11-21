package guest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	devContainerCLI       = "/usr/local/bin/devcontainer"
	devContainerOutputMax = 1 << 20
	developmentLabel      = "shiftbox.container=development"
	credentialDirectory   = "/run/shiftbox-credentials"
	ideDirectory          = "/run/shiftbox-ide"
)

var dockerContainerID = regexp.MustCompile(`^[0-9a-f]{64}$`)

type devContainer struct {
	workspace               string
	config                  string
	repository              *repositoryPreparation
	credentials             bool
	imageCache              bool
	applyImageCache         func(string) error
	ide                     bool
	codeServerSource        string
	retainHome              bool
	repairImageFile         string
	repairSocketDir         string
	run                     func(context.Context, string, ...string) ([]byte, error)
	sshExec                 func(context.Context, ...string) *exec.Cmd
	setProjectOwner         func(context.Context, string, int, int) error
	timing                  func(phase string, elapsed time.Duration, outcome string)
	launchTiming            chan<- launchTimingReport
	logQueue                chan<- workspaceLogOutput
	logOutput               func(string)
	detailedConfigureTiming bool
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
		WaitFor    string `json:"waitFor"`
		RemoteUser string `json:"remoteUser"`
		// The CLI returns one entry per contributing image, Feature, or project.
		Customizations struct {
			VSCode []vsCodeCustomization `json:"vscode"`
		} `json:"customizations"`
	} `json:"mergedConfiguration"`
}

func (d devContainer) ensure(ctx context.Context) (containerConnection, error) {
	return d.configure(ctx, rebuildSource{})
}

func (d devContainer) configure(ctx context.Context, previous rebuildSource) (containerConnection, error) {
	if d.imageCache {
		started := time.Now()
		err := d.prepareImage(ctx)
		d.measurePhase("image_prepare", started, err)
		if err != nil {
			return containerConnection{}, err
		}
	}
	// Let the reference CLI read JSONC, resolve Features and image metadata,
	// and execute lifecycle commands. The guest VM is the isolation boundary.
	options := []string{"--skip-non-blocking-commands", "--skip-post-attach", "--include-merged-configuration", "--update-remote-user-uid-default", "off"}
	if d.repository != nil || previous.id != "" {
		options = append(options, "--skip-post-create")
	}
	if previous.id != "" {
		options = append(options, "--remove-existing-container")
	}
	result, err := d.up(ctx, "initial_devcontainer_setup", options...)
	if err != nil {
		return containerConnection{}, err
	}
	if !dockerContainerID.MatchString(result.ID) || result.Merged == nil {
		return containerConnection{}, errors.New("Dev Container CLI did not return connection evidence")
	}
	if previous.id != "" {
		if err := d.verifyReplacement(ctx, result, previous); err != nil {
			return containerConnection{}, err
		}
	} else if d.retainHome {
		started := time.Now()
		result, err = d.ensureRetainedHome(ctx, result, options)
		d.measurePhase("retained_home", started, err)
		if err != nil {
			return containerConnection{}, err
		}
	}
	waitFor := result.Merged.WaitFor
	if waitFor == "" {
		waitFor = "updateContentCommand"
	}
	if !validWaitFor(waitFor) {
		return containerConnection{}, errors.New("invalid Dev Container waitFor stage")
	}
	connection := containerConnection{ID: result.ID, WaitFor: waitFor}
	if d.repository != nil {
		started := time.Now()
		err := d.prepareProjectOwner(ctx, connection.ID, result.Merged.RemoteUser)
		d.measurePhase("project_permissions", started, err)
		if err != nil {
			return containerConnection{}, err
		}
	}
	if d.repository != nil || previous.id != "" {
		started := time.Now()
		_, err := d.invoke(ctx, "run-user-commands", "--container-id", connection.ID, "--skip-non-blocking-commands", "--skip-post-attach")
		d.measurePhase("required_hooks", started, err)
		if err != nil {
			return containerConnection{}, err
		}
	}
	if d.ide {
		if err := d.startIDE(ctx, connection.ID, result.Merged.Customizations.VSCode); err != nil {
			return containerConnection{}, err
		}
	}
	return connection, nil
}

func (d devContainer) finishSetup(ctx context.Context, connection containerConnection) error {
	if !dockerContainerID.MatchString(connection.ID) || !validWaitFor(connection.WaitFor) {
		return errors.New("invalid Dev Container connection evidence")
	}
	// The CLI tracks completed creation and start hooks in the container.
	// Attach hooks belong to the IDE attach path.
	started := time.Now()
	_, err := d.invoke(ctx, "run-user-commands", "--container-id", connection.ID, "--skip-post-attach")
	d.measurePhase("remaining_hooks", started, err)
	return err
}

func (d devContainer) up(ctx context.Context, phase string, options ...string) (devContainerResult, error) {
	started := time.Now()
	result, err := d.invoke(ctx, "up", options...)
	d.measurePhase(phase, started, err)
	return result, err
}

func (d devContainer) invoke(ctx context.Context, stage string, options ...string) (devContainerResult, error) {
	output, err := d.command(ctx, stage, options...)
	if err != nil {
		return devContainerResult{}, err
	}
	var result devContainerResult
	if len(output) > devContainerOutputMax || json.Unmarshal(output, &result) != nil || result.Outcome != "success" {
		return devContainerResult{}, errors.New("Dev Container CLI " + stage + " did not report success")
	}
	return result, nil
}

func (d devContainer) command(ctx context.Context, stage string, options ...string) ([]byte, error) {
	if !filepath.IsAbs(d.workspace) {
		return nil, errors.New("invalid Dev Container workspace path")
	}
	// Use the same label for creation and continuation so the CLI does not
	// append a second copy of project hooks to the saved container metadata.
	dockerPath := "/usr/bin/docker"
	if stage == "up" && d.ide {
		var err error
		dockerPath, err = d.codeServerDocker()
		if err != nil {
			return nil, errBrowserIDE
		}
		defer os.Remove(dockerPath)
	}
	args := d.commandBase(stage, dockerPath, "json")
	if stage == "up" && d.ide {
		args = append(args, "--mount", "type=bind,source="+ideDirectory+",target="+ideDirectory)
	}
	if stage == "up" && d.retainHome {
		home, err := d.retainedHome()
		if err != nil {
			return nil, err
		}
		if home != "" {
			args = append(args, "--mount", "type=bind,source="+d.retainedHomeData()+",target="+home)
		}
	}
	if stage == "up" && d.credentials {
		// Mount the directory so an agent restart can replace its listener.
		// The command file is a runtime copy, separate from the agent binary.
		args = append(args,
			"--mount", "type=bind,source="+credentialDirectory+",target="+credentialDirectory,
			"--mount", "type=bind,source="+credentialDirectory+"/shiftbox-launcher,target=/usr/local/bin/shiftbox")
	}
	if d.config != "" {
		if !validDevContainerPath(d.config) {
			return nil, errors.New("invalid Dev Container configuration path")
		}
		args = append(args, "--config", filepath.Join(d.workspace, d.config))
	}
	output, err := d.runner()(ctx, devContainerCLI, append(args, options...)...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Dev Container CLI " + stage + " failed")
	}
	return output, nil
}

func (d devContainer) commandBase(stage, dockerPath, format string) []string {
	return []string{stage, "--workspace-folder", d.workspace, "--docker-path", dockerPath,
		"--docker-compose-path", "/bin/false", "--log-format", format, "--id-label", developmentLabel}
}

func (d devContainer) runner() func(context.Context, string, ...string) ([]byte, error) {
	if d.run != nil {
		return d.run
	}
	return func(ctx context.Context, executable string, args ...string) ([]byte, error) {
		if executable != devContainerCLI || len(args) == 0 {
			return runDevContainerCLI(ctx, executable, args...)
		}
		var stderr io.Writer
		if d.timing != nil && args[0] == "up" {
			dockerPath := "/usr/bin/docker"
			if index := slices.Index(args, "--docker-path"); index >= 0 && index+1 < len(args) {
				dockerPath = args[index+1]
			}
			stderr = &devContainerCLITimings{
				dockerPath: dockerPath,
				record: func(name string, elapsed time.Duration) {
					d.timing(name, elapsed, "observed")
				},
			}
		}
		if d.logOutput != nil {
			d.logOutput("=== devcontainer " + args[0] + " ===\n")
			logs := &devContainerLogs{record: d.logOutput}
			defer logs.flush()
			if stderr == nil {
				stderr = logs
			} else {
				stderr = io.MultiWriter(stderr, logs)
			}
		}
		return runDevContainerCommand(ctx, executable, stderr, args...)
	}
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
	return runDevContainerCommand(ctx, executable, nil, args...)
}

func runDevContainerCommand(ctx context.Context, executable string, stderr io.Writer, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stderr = stderr
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

// Write captures command output up to the diagnostic limit.
func (o *devContainerOutput) Write(data []byte) (int, error) {
	if len(data) > devContainerOutputMax-o.buffer.Len() {
		return 0, errors.New("Dev Container CLI response exceeds the output limit")
	}
	return o.buffer.Write(data)
}
