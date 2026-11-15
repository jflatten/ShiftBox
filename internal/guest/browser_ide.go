package guest

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
    "$server/lib/node" -e 'const c = require("node:net").connect(process.argv[1]); c.setTimeout(1000, () => process.exit(1)); c.on("connect", () => c.destroy()); c.on("error", () => process.exit(1));' "$socket"
}
if socket_ready; then
    exit 0
fi
nohup "$server/bin/code-server" --config /dev/null --auth none --socket "$socket" --socket-mode 0600 \
    --disable-proxy --disable-update-check --disable-telemetry --ignore-last-opened -- "$(pwd)" </dev/null >/dev/null 2>&1 &
pid=$!
for attempt in $(seq 1 30); do
    if socket_ready; then exit 0; fi
    kill -0 "$pid" || exit 1
    sleep 1
done
kill "$pid" 2>/dev/null || true
exit 1
`

func (d devContainer) startIDE(ctx context.Context, id string) error {
	started := time.Now()
	err := d.installCodeServer(ctx, id)
	d.measurePhase("ide_install", started, err)
	if err != nil {
		return err
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

// installCodeServer copies the pinned distribution into the container's own
// layers. The guest keeps one receipt for the current development container.
func (d devContainer) installCodeServer(ctx context.Context, id string) error {
	source := d.codeServerSource
	if source == "" {
		source = codeServerPackage
	}
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
