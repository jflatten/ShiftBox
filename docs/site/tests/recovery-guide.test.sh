#!/usr/bin/env bash
# Checks the operator guide against the supported recovery commands and safety order.
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)
python3 - "$root" <<'PY'
from pathlib import Path
import re
import subprocess
import sys
from textwrap import dedent

root = Path(sys.argv[1])
guide = (root / "docs/site/upgrade-recovery.md").read_text()
assert re.search(r"^layout: default$", guide, re.M)
assert re.search(r"^permalink: /upgrade-recovery/$", guide, re.M)

# Checkpoint evidence precedes stack review and any schema change.
headings = list(re.finditer(r"^## (.+)$", guide, re.M))
sections = {
    match[1]: guide[match.end():headings[index + 1].start() if index + 1 < len(headings) else len(guide)]
    for index, match in enumerate(headings)
}
required = [
    "Verify a management backup", "Review the upgrade change set",
    "Recover from a failed migration", "Rehearse the recovery path",
]
assert [name for name in sections if name in required] == required
backup = sections[required[0]]
for term in ("Backup & Recovery", "verified", "Dex", "installation key", "workspace VM disks"):
    assert term.casefold() in backup.casefold(), f"Missing backup guidance for {term}"
update = sections[required[1]]
for term in ("all-retained-workspaces-current", "retained EBS volume", "stack policy", "same stack", "change set"):
    assert term.casefold() in update.casefold(), f"Missing update check for {term}"

# Recovery reuses the archive identity and requires review before ordinary authority resumes.
recovery = sections[required[2]]
for term in (
    "matching controller image", "empty replacement PostgreSQL", "configuration.json", "administrator",
    "provider", "authorization and retention", "fresh sign-in", "sessions", "grants"
):
    assert term.casefold() in recovery.casefold(), f"Missing recovery check for {term}"
for term in ("disposable installation", "same controller build", "migration gates"):
    assert term.casefold() in sections[required[3]].casefold(), f"Missing rehearsal scope for {term}"

# Verify the documented ShiftBox commands use flags accepted by the actual CLI.
sources = {
    "backup": ("cmd/shiftbox/backup.go", {"config", "output"}),
    "restore validate": ("cmd/shiftbox/restore.go", {
        "config", "archive", "installation-id", "release", "destination", "postgres-config",
    }),
    "restore activate": ("cmd/shiftbox/restore.go", {
        "config", "archive", "validation-id", "destination", "postgres-config",
    }),
    "serve": ("cmd/shiftbox/serve.go", {"config"}),
}
blocks = [dedent(block) for block in re.findall(r"```sh\n(.*?)```", guide, re.S)]
for text in blocks:
    subprocess.run(["sh", "-n"], input=text, text=True, check=True)
commands = "\n".join(blocks).replace("\\\n", " ")
for command, (file, flags) in sources.items():
    match = re.search(rf"^shiftbox {command} (.+)$", commands, re.M)
    assert match, f"Missing supported command: {command}"
    used = set(re.findall(r"--([a-z-]+)\b", match[1]))
    assert flags <= used, f"Missing flags for {command}: {flags - used}"
    code = (root / file).read_text()
    assert all(f'flags.String("{flag}"' in code for flag in used), f"An option is not supported by {command}"
assert "aws cloudformation describe-events" in commands
assert "aws cloudformation describe-change-set" in commands

# Versioned public docs link to their companion public pages rather than private evidence.
for slug in ("install", "troubleshooting"):
    assert f"'/{slug}/' | relative_url" in guide
    target = root / f"docs/site/{slug}.md"
    assert re.search(rf"^permalink: /{slug}/$", target.read_text(), re.M)
print("Upgrade and recovery guide checks passed.")
PY
