package guest

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
)

type vsCodeCustomization struct {
	Settings   map[string]json.RawMessage `json:"settings"`
	Extensions []string                   `json:"extensions"`
}

const codeServerExtensionTimeout = 5 * time.Minute

var (
	extensionID      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*\.[a-z0-9][a-z0-9-]*$`)
	extensionVersion = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.+-]*$`)
)

// applyCodeServerSettings merges project settings into the remote Machine file,
// using the same home and XDG defaults as the bundled code-server CLI.
const applyCodeServerSettings = `
const fs = require('node:fs');
const path = require('node:path');
const { paths } = require('` + codeServerDir + `/out/node/util.js');
const directory = path.join(paths.data, 'Machine');
const file = path.join(directory, 'settings.json');
let original = '{}';
try {
    original = fs.readFileSync(file, 'utf8');
} catch (error) {
    if (error.code !== 'ENOENT') throw error;
}
// VS Code settings permit comments and trailing commas. Leave quoted text intact.
const json = original.replace(/"(?:[^"\\]|\\[\s\S])*"|\/\/[^\r\n]*|\/\*[\s\S]*?\*\//g,
    token => token.startsWith('/') ? ' ' : token).replace(/"(?:[^"\\]|\\[\s\S])*"|,\s*(?=[}\]])/g,
    token => token.startsWith(',') ? '' : token);
const existing = JSON.parse(json.trim() || '{}');
if (!existing || Array.isArray(existing) || typeof existing !== 'object') {
    throw new Error('Machine settings must be a JSON object');
}
const merged = { ...existing, ...JSON.parse(process.argv[1]) };
if (JSON.stringify(merged) !== JSON.stringify(existing)) {
    fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
    const temp = fs.mkdtempSync(path.join(directory, '.shiftbox-settings-'));
    try {
        const staged = path.join(temp, 'settings.json');
        fs.writeFileSync(staged, JSON.stringify(merged, null, 4) + '\n', { mode: 0o600 });
        fs.renameSync(staged, file);
    } finally {
        fs.rmSync(temp, { recursive: true, force: true });
    }
}
`

// applyVSCodeCustomizations runs inside the development container as its remote
// user. An editor customization failure is recoverable without entering repair.
func (d devContainer) applyVSCodeCustomizations(ctx context.Context, id string, customizations []vsCodeCustomization) {
	if len(customizations) == 0 {
		return
	}
	settings := make(map[string]json.RawMessage)
	extensions := make(map[string]string)
	for _, customization := range customizations {
		maps.Copy(settings, customization.Settings)
		for _, extension := range customization.Extensions {
			name, excluded := strings.CutPrefix(extension, "-")
			identifier, version, versioned := strings.Cut(name, "@")
			identifier = strings.ToLower(identifier)
			if !extensionID.MatchString(identifier) || (versioned && !extensionVersion.MatchString(version)) {
				slog.WarnContext(ctx, "Ignoring invalid Dev Container extension identifier")
				continue
			}
			if excluded {
				delete(extensions, identifier)
				continue
			}
			extensions[identifier] = identifier
			if versioned {
				extensions[identifier] += "@" + version
			}
		}
	}
	if len(settings) != 0 {
		started := time.Now()
		data, err := json.Marshal(settings)
		if err == nil {
			settingsCtx, cancel := context.WithTimeout(ctx, codeServerStartTimeout)
			_, err = d.command(settingsCtx, "exec", "--container-id", id, "--",
				codeServerDir+"/lib/node", "-e", applyCodeServerSettings, "--", string(data))
			cancel()
		}
		d.measurePhase("ide_settings", started, err)
		if err != nil {
			slog.WarnContext(ctx, "Could not apply Dev Container VS Code settings", "err", err)
		}
	}
	if len(extensions) == 0 {
		return
	}
	requested := slices.Sorted(maps.Values(extensions))
	args := []string{"--container-id", id, "--", codeServerDir + "/bin/code-server", "--config=/dev/null", "--disable-telemetry"}
	for _, extension := range requested {
		args = append(args, "--install-extension="+extension)
	}
	installCtx, cancel := context.WithTimeout(ctx, codeServerExtensionTimeout)
	defer cancel()
	started := time.Now()
	_, err := d.command(installCtx, "exec", args...)
	d.measurePhase("ide_extensions", started, err)
	if err != nil {
		slog.WarnContext(ctx, "Could not install all Dev Container VS Code extensions", "extensions", requested, "err", err)
	}
}
