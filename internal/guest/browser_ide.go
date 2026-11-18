package guest

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/proxy"
)

const (
	codeServerDir          = "/opt/shiftbox-code-server"
	codeServerPackage      = "/usr/local/lib/shiftbox/code-server"
	codeServerCopyTimeout  = 5 * time.Minute
	codeServerStartTimeout = 2 * time.Minute
)

var errBrowserIDE = errors.New("browser IDE startup failed")

// The CLI runs this command as the configured remote user in the configured
// workspace directory. The workbench uses that user's normal settings and home.
const startCodeServer = `set -eu
socket=` + proxy.IDESocket + `
server=` + codeServerDir + `
socket_ready() {
    [ -S "$socket" ] || return 1
    if command -v curl >/dev/null 2>&1; then
        curl -q -fsS --max-time 1 --noproxy '*' --unix-socket "$socket" http://localhost/healthz >/dev/null 2>&1 && return 0
    fi
    "$server/lib/node" -e 'const c = require("node:net").connect(process.argv[1]); c.setTimeout(1000, () => process.exit(1)); c.on("connect", () => c.destroy()); c.on("error", () => process.exit(1));' "$socket"
}
if socket_ready; then
    exit 0
fi
nohup "$server/bin/code-server" --config /dev/null --auth none --socket "$socket" --socket-mode 0600 \
    --disable-proxy --disable-update-check --disable-telemetry --ignore-last-opened -- "$(pwd)" </dev/null >/dev/null 2>&1 &
pid=$!
for attempt in $(seq 1 150); do
    if socket_ready; then exit 0; fi
    kill -0 "$pid" || exit 1
    sleep 0.2
done
kill "$pid" 2>/dev/null || true
exit 1
`

func (d devContainer) startIDE(ctx context.Context, id string) error {
	started := time.Now()
	mounted, err := d.codeServerMounted(ctx, id)
	d.measurePhase("ide_mount", started, err)
	if err != nil {
		return errors.Join(errBrowserIDE, ctx.Err())
	}
	if !mounted {
		started = time.Now()
		err := d.installCodeServer(ctx, id)
		d.measurePhase("ide_install", started, err)
		if err != nil {
			return err
		}
	}
	startCtx, cancel := context.WithTimeout(ctx, codeServerStartTimeout)
	defer cancel()
	started = time.Now()
	_, err = d.command(startCtx, "exec", "--container-id", id, "/bin/sh", "-c", startCodeServer)
	d.measurePhase("ide_start", started, err)
	if err != nil {
		return errors.Join(errBrowserIDE, startCtx.Err())
	}
	return nil
}

func (d devContainer) codeServerBundle() string {
	if d.codeServerSource != "" {
		return d.codeServerSource
	}
	return codeServerPackage
}

// codeServerDocker creates a short-lived launcher in the guest's private state
// directory. The pinned Dev Container CLI's --mount option drops read-only
// flags, so pass the bundle mount directly to Docker for the development run.
func (d devContainer) codeServerDocker() (string, error) {
	file, err := os.CreateTemp(filepath.Dir(d.workspace), ".devcontainer-docker-")
	if err != nil {
		return "", err
	}
	mount := "type=bind,source=" + d.codeServerBundle() + ",target=" + codeServerDir + ",readonly"
	mount = "'" + strings.ReplaceAll(mount, "'", "'\\''") + "'"
	_, writeErr := file.WriteString(`#!/bin/sh
if [ "${1-}" = run ]; then
    previous=
    for argument do
        case "$previous:$argument" in
            -l:` + developmentLabel + `|--label:` + developmentLabel + `)
                shift
                set -- run --mount ` + mount + ` "$@"
                break
                ;;
        esac
        previous=$argument
    done
fi
exec /usr/bin/docker "$@"
`)
	if err := errors.Join(writeErr, file.Chmod(0700), file.Close()); err != nil {
		os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}

// codeServerMounted distinguishes a legacy container from a new container with
// the expected source and read-only mount verified by Docker.
func (d devContainer) codeServerMounted(ctx context.Context, id string) (bool, error) {
	mounts, err := containerMounts(ctx, id, d.runner())
	if err != nil {
		return false, err
	}
	for _, mount := range mounts {
		if mount.Destination != codeServerDir {
			continue
		}
		if mount.Type == "bind" && mount.Source == d.codeServerBundle() && !mount.RW {
			return true, nil
		}
		return false, errBrowserIDE
	}
	return false, nil
}

// installCodeServer supports existing containers created before the bundle
// mount was introduced. Their next rebuild uses the read-only bind mount.
func (d devContainer) installCodeServer(ctx context.Context, id string) error {
	source := d.codeServerBundle()
	metadata, err := os.ReadFile(filepath.Join(source, "package.json"))
	if err != nil || len(metadata) == 0 {
		return errBrowserIDE
	}
	// The platform pins the distribution. Its package manifest identifies that
	// version without reading the large files on every start.
	receipt := fmt.Appendf(nil, "%s %x\n", id, sha256.Sum256(metadata))
	path := filepath.Join(filepath.Dir(d.workspace), "code-server-install")
	installed, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errBrowserIDE
	}
	if string(installed) == string(receipt) {
		return nil
	}
	// Invalidate the old receipt before changing any files. If the copy is
	// interrupted, even a rollback to the old package must install it again.
	if len(installed) != 0 {
		if err := writePrivateState(path, nil); err != nil {
			return errBrowserIDE
		}
	}
	copyCtx, cancel := context.WithTimeout(ctx, codeServerCopyTimeout)
	defer cancel()
	if _, err := d.runner()(copyCtx, "/usr/bin/docker", "cp", source+"/.", id+":"+codeServerDir); err != nil {
		return errors.Join(errBrowserIDE, copyCtx.Err())
	}
	if err := writePrivateState(path, receipt); err != nil {
		return errBrowserIDE
	}
	return nil
}
