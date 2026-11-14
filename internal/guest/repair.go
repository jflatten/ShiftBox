package guest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	enterRepairCommand  = "enter-repair"
	exitRepairCommand   = "exit-repair"
	repairLabel         = "shiftbox.container=repair"
	problemRepairFailed = "guest.repair_failed"
	repairDirectory     = "/run/shiftbox-repair"
	repairSocketName    = "repair.sock"
)

// The repair workbench starts with a fresh home and a restricted workspace.
// It runs directly from the preloaded image, outside the Dev Container CLI.
const runRepairIDE = `set -eu
umask 077
mkdir -p "$HOME/data/User" "$HOME/extensions"
printf '%s\n' '{"security.workspace.trust.startupPrompt":"never","task.allowAutomaticTasks":"off","extensions.ignoreRecommendations":true}' > "$HOME/data/User/settings.json"
exec /opt/shiftbox-code-server/bin/code-server --config /dev/null --auth none \
    --socket ` + repairDirectory + `/` + repairSocketName + ` --socket-mode 0600 \
    --disable-proxy --disable-update-check --disable-telemetry --ignore-last-opened \
    --user-data-dir "$HOME/data" --extensions-dir "$HOME/extensions" /project
`

const probeRepairIDE = `const net = require('node:net');
let attempts = 30;
function probe() {
  const socket = net.connect('` + repairDirectory + `/` + repairSocketName + `');
  socket.setTimeout(1000, () => socket.destroy(new Error('timeout')));
  socket.on('connect', () => socket.end());
  socket.on('error', () => { if (--attempts) setTimeout(probe, 1000); else process.exitCode = 1; });
}
probe();`

func (j *localJournal) repair(ctx context.Context, dispatch guestDispatch, runtime devContainer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := j.accept(dispatch); err != nil {
		return err
	}
	next := *j.state.Operation
	if next.Phase == localSucceeded || next.Phase == localFailed {
		return nil
	}
	next.Phase = localRequired
	if err := j.saveOperation(next); err != nil {
		return err
	}
	workCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var id string
	var err error
	if dispatch.Command == enterRepairCommand {
		id, err = runtime.enterRepair(workCtx)
	} else {
		err = runtime.exitRepair(workCtx)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		next.Phase, next.Problem = localFailed, problemRepairFailed
	} else {
		next.Phase, next.Connection.ID = localSucceeded, id
	}
	return j.saveOperation(next)
}

func (d devContainer) enterRepair(ctx context.Context) (string, error) {
	project, err := os.Lstat(d.workspace)
	if err != nil || !project.IsDir() || !filepath.IsAbs(d.workspace) || !filepath.IsAbs(d.repairSocketDir) {
		return "", errors.New("repair requires a retained checkout and guest socket directory")
	}
	owner, ok := project.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("cannot determine retained checkout owner")
	}
	data, err := os.ReadFile(d.repairImageFile)
	image, valid := strings.CutPrefix(strings.TrimSpace(string(data)), "sha256:")
	if err != nil || !valid || !dockerContainerID.MatchString(image) {
		return "", errors.New("preloaded repair image identity is unavailable")
	}
	image = "sha256:" + image
	// Quiesce the project workload before exposing its editable checkout.
	stopCtx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	err = d.stopWorkload(stopCtx)
	cancel()
	if err != nil {
		return "", err
	}
	id, err := d.repairContainer(ctx)
	if err != nil {
		return "", err
	}
	if id != "" {
		out, err := d.runner()(ctx, "/usr/bin/docker", "container", "inspect", "--format", "{{.Image}} {{.State.Running}}", "--", id)
		if err != nil || strings.TrimSpace(string(out)) != image+" true" {
			return "", errors.New("repair container is not running with the preloaded image")
		}
	} else {
		if err := d.removeRepairSocket(); err != nil {
			return "", err
		}
		out, err := d.runner()(ctx, "/usr/bin/docker", "container", "run", "--detach", "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL",
			"--name", "shiftbox-repair", "--label", repairLabel, "--security-opt", "no-new-privileges",
			"--user", fmt.Sprintf("%d:%d", owner.Uid, owner.Gid), "--env", "HOME=/tmp/shiftbox-repair-home", "--tmpfs", "/tmp",
			"--mount", "type=bind,src="+d.workspace+",dst=/project",
			"--mount", "type=bind,src="+d.repairSocketDir+",dst="+repairDirectory,
			"--", image, "/bin/sh", "-c", runRepairIDE)
		id = strings.TrimSpace(string(out))
		if err != nil || !dockerContainerID.MatchString(id) {
			return "", errors.New("could not start the preloaded repair container")
		}
	}
	if _, err := d.runner()(ctx, "/usr/bin/docker", "container", "exec", "--", id, codeServerDir+"/lib/node", "-e", probeRepairIDE); err != nil {
		return "", errors.New("repair IDE socket did not become ready")
	}
	return id, nil
}

func (d devContainer) repairContainer(ctx context.Context) (string, error) {
	out, err := d.runner()(ctx, "/usr/bin/docker", "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label="+repairLabel)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if id != "" && !dockerContainerID.MatchString(id) {
		return "", errors.New("unexpected repair container inventory")
	}
	return id, nil
}

func (d devContainer) exitRepair(ctx context.Context) error {
	id, err := d.repairContainer(ctx)
	if err != nil {
		return err
	}
	if id != "" {
		if _, err := d.runner()(ctx, "/usr/bin/docker", "container", "stop", "--timeout", "10", "--", id); err != nil {
			return err
		}
		if _, err := d.runner()(ctx, "/usr/bin/docker", "container", "rm", "--", id); err != nil {
			return err
		}
	}
	return d.removeRepairSocket()
}

func (d devContainer) removeRepairSocket() error {
	if !filepath.IsAbs(d.repairSocketDir) {
		return errors.New("invalid repair socket directory")
	}
	err := os.Remove(filepath.Join(d.repairSocketDir, repairSocketName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
