package guest

import (
	"context"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/proxy"
)

const codeServerDir = "/opt/shiftbox-code-server"

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
	run := d.run
	if run == nil {
		run = runDevContainerCLI
	}
	// The CLI mount option handles source and target only. Copy the packaged
	// distribution so the container can never mutate the guest's install.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := run(ctx, "/usr/bin/docker", "cp", "/usr/local/lib/shiftbox/code-server/.", id+":"+codeServerDir); err != nil {
		if ctx.Err() != nil {
			return errors.Join(errBrowserIDE, ctx.Err())
		}
		return errBrowserIDE
	}
	_, err := d.command(ctx, "exec", "--container-id", id, "/bin/sh", "-c", startCodeServer)
	if err != nil {
		if ctx.Err() != nil {
			return errors.Join(errBrowserIDE, ctx.Err())
		}
		return errBrowserIDE
	}
	return nil
}
