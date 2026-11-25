"""Check that host boot can retry TLS provisioning without replacing trust."""

import os
from pathlib import Path
import subprocess


def test_private_install_reuses_certificates_and_separates_trust(tmp_path):
    # Host replacement reuses the same keys, with independent browser and database trust.
    script = Path(__file__).parents[1] / "cloudformation" / "aws" / "host" / "installation-tls.sh"
    root = tmp_path / "persist"
    root.mkdir()
    commands = tmp_path / "bin"
    commands.mkdir()
    for command, contents in {
        "aws": '#!/bin/sh\ncase "$*" in *put-secret-value*file://*/browser-identity/ca.crt*) exit 0;; esac\nexit 1\n',
        "chown": "#!/bin/sh\nexit 0\n",
    }.items():
        executable = commands / command
        executable.write_text(contents)
        executable.chmod(0o755)
    env = os.environ | {"PATH": f"{commands}:{os.environ['PATH']}"}
    command = ["sh", str(script), str(root), "fixture.internal", "us-east-2", "fixture-secret"]
    subprocess.run(command, env=env, check=True, capture_output=True)
    files = sorted(root.rglob("*.crt"))
    before = {path: path.read_bytes() for path in files}
    assert {path.relative_to(root).as_posix() for path in before} == {
        "browser-identity/ca.crt", "browser-identity/server.crt",
        "pg-identity/ca.crt", "pg-identity/server.crt", "database-trust/ca.crt",
    }
    browser_ca = root / "browser-identity" / "ca.crt"
    postgres_ca = root / "database-trust" / "ca.crt"
    postgres = root / "pg-identity" / "server.crt"
    assert browser_ca.read_bytes() != postgres_ca.read_bytes()
    assert not list(root.rglob("ca.key"))
    assert subprocess.run(["openssl", "verify", "-CAfile", str(browser_ca), str(postgres)],
                          capture_output=True).returncode != 0
    subprocess.run(command, env=env, check=True, capture_output=True)
    assert {path: path.read_bytes() for path in files} == before
