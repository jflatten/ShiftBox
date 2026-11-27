"""Verify manual main dispatch and rejection of a moved source tip."""

import os
from pathlib import Path
import subprocess

import pytest
import yaml


ROOT = Path(__file__).resolve().parents[2]
SOURCE = "a" * 40


def test_workflow_has_only_manual_entry_and_confines_artifacts():
    # Public Actions artifacts and release jobs must be outside this test workflow.
    source = (ROOT / ".github/workflows/deploy-main-aws.yml").read_text()
    workflow = yaml.load(source, Loader=yaml.BaseLoader)
    assert set(workflow["on"]) == {"workflow_dispatch"}
    assert workflow["jobs"]["deploy"]["needs"] == "helper"
    assert {job["environment"] for job in workflow["jobs"].values()} == {"aws-testing"}
    for job in workflow["jobs"].values():
        checkout = next(step for step in job["steps"] if step.get("uses", "").startswith("actions/checkout@"))
        assert checkout["with"] == {"ref": "${{ github.sha }}", "persist-credentials": "false"}
    assert "upload-artifact" not in source and "release.yml" not in source


@pytest.fixture
def dispatch_env(tmp_path):
    git = tmp_path / "git"
    git.write_text("""#!/bin/sh
case "$1" in
  rev-parse) printf '%s\\n' "$GITHUB_SHA" ;;
  status) ;;
  ls-remote) printf '%s\\trefs/heads/main\\n' "$MOCK_MAIN_SHA" ;;
  *) exit 1 ;;
esac
""")
    git.chmod(0o700)
    return {
        "PATH": f"{tmp_path}:{os.environ['PATH']}",
        "GITHUB_REPOSITORY": "jflatten/ShiftBox", "GITHUB_EVENT_NAME": "workflow_dispatch",
        "GITHUB_REF": "refs/heads/main", "GITHUB_ACTOR_ID": "11561330", "GITHUB_TRIGGERING_ACTOR": "jflatten",
        "GITHUB_SHA": SOURCE, "GITHUB_RUN_ID": "1234", "GITHUB_RUN_ATTEMPT": "1", "MOCK_MAIN_SHA": SOURCE,
    }


@pytest.mark.parametrize(("remote_source", "expected"), [(SOURCE, 0), ("b" * 40, 1)])
def test_main_tip_guard(remote_source, expected, dispatch_env):
    # A queued dispatch that no longer matches origin/main must stop before any build.
    env = dispatch_env | {"MOCK_MAIN_SHA": remote_source}
    result = subprocess.run(["bash", ROOT / "platform/development/main-test.sh", "source"],
                            env=env, capture_output=True, text=True, check=False)
    assert result.returncode == expected, result.stderr
    if expected:
        assert "main moved after dispatch" in result.stderr


def test_helper_staging_calls_private_s3_with_valid_arguments(tmp_path, dispatch_env):
    # The workflow must stage the signed-in runner's helper with encryption in its versioned bucket.
    aws = tmp_path / "aws"
    aws.write_text("""#!/bin/sh
case " $* " in *' + '*) exit 2 ;; esac
case "$4 $5" in
  "sts get-caller-identity") echo 888293065132 ;;
  "s3api get-bucket-location") echo us-east-2 ;;
  "s3api get-public-access-block")
    echo '{"PublicAccessBlockConfiguration":{"BlockPublicAcls":true,"BlockPublicPolicy":true,"IgnorePublicAcls":true,"RestrictPublicBuckets":true}}'
    ;;
  "s3api get-bucket-versioning") echo Enabled ;;
  "s3api put-object")
    case " $* " in
      *" --key main-test/1234/1/ShiftBox-Local-Helper.dmg "*" --server-side-encryption AES256 "*)
        echo '{"VersionId":"helper-v1"}' ;;
      *) exit 2 ;;
    esac
    ;;
  *) exit 2 ;;
esac
""")
    aws.chmod(0o700)
    helper = tmp_path / "helper.dmg"
    helper.write_bytes(b"test helper bytes")
    output = tmp_path / "output"
    env = dispatch_env | {"TEST_BUCKET": "shiftbox-test-fixture", "GITHUB_OUTPUT": str(output)}
    result = subprocess.run(["bash", ROOT / "platform/development/main-test.sh", "stage-helper", helper],
                            env=env, capture_output=True, text=True, check=False)
    assert result.returncode == 0, result.stderr
    assert output.read_text().splitlines()[0] == "version=helper-v1"
