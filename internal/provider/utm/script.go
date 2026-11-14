package utm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"time"

	"github.com/jflatten/ShiftBox/internal/provider"
)

// inventoryScript uses UTM's registered application dictionary. Reading
// configuration is required for ownership notes and the full drive list.
const inventoryScript = `
function run(argv) {
    const utm = Application("/Applications/UTM.app");
    const version = utm.utmVersion();
    if (version !== argv[0]) return JSON.stringify({version});
    const machines = utm.virtualMachines().map(vm => {
        const id = vm.id();
        const backend = vm.backend();
        const status = vm.status();
        const configuration = vm.configuration();
        const notes = configuration.notes || "";
        if (!notes.startsWith("shiftbox:")) return {id, notes};
        return {
            id, backend, status, notes,
            architecture: configuration.architecture,
            cpu: configuration.cpuCores, memoryMiB: configuration.memory,
            drives: configuration.drives.map(drive => ({
                id: drive.id, guestMiB: drive.guestSize || 0, removable: drive.removable
            })),
            networkModes: configuration.networkInterfaces.map(network => network.mode)
        };
    });
    return JSON.stringify({version, machines});
}
`

// Inspector reads the supported local scripting surface for an exact UTM version.
type Inspector struct {
	Version string
}

func (i Inspector) Inspect(ctx context.Context, ref provider.WorkspaceRef) (provider.Inventory, error) {
	if i.Version == "" {
		return provider.Inventory{}, provider.ErrCapabilityUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", inventoryScript, "--", i.Version)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return provider.Inventory{}, errors.Join(provider.ErrUnavailable, ctx.Err())
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && bytes.Contains(exit.Stderr, []byte("(-1743)")) {
			return provider.Inventory{}, provider.ErrPermission
		}
		return provider.Inventory{}, provider.ErrUnavailable
	}
	var result struct {
		Version  string
		Machines []vmRecord
	}
	if err := json.Unmarshal(out, &result); err != nil || result.Version == "" {
		return provider.Inventory{}, provider.ErrOutcomeUnknown
	}
	if result.Version != i.Version {
		return provider.Inventory{}, provider.ErrCapabilityUnsupported
	}
	if result.Machines == nil {
		return provider.Inventory{}, provider.ErrOutcomeUnknown
	}
	return normalizeInventory(ref, result.Machines)
}
