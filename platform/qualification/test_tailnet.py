"""Exercise tailnet authorization, run identity, and cleanup with local HTTP fixtures."""
import copy
import datetime
import json
import unittest
from unittest import mock
from urllib.parse import parse_qs

import tailnet


NOW = datetime.datetime(2026, 11, 22, 1, 15, tzinfo=datetime.timezone.utc)
RUN = "123"
ATTEMPT = "2"
TAG = "tag:shiftbox-qualification"
NODE = "n5SUKe8CNTRL"
HOSTNAME = "sbq-123-2-router"
TAILNET_ID = "tTestTailnet"
ENV = {
    "GITHUB_REPOSITORY": "jflatten/ShiftBox",
    "GITHUB_REF": "refs/heads/main",
    "GITHUB_WORKFLOW_REF": "jflatten/ShiftBox/.github/workflows/qualify-aws.yml@refs/heads/main",
    "GITHUB_EVENT_NAME": "workflow_dispatch",
    "GITHUB_ACTOR": "jflatten",
    "GITHUB_TRIGGERING_ACTOR": "jflatten",
    "GITHUB_JOB": "cleanup",
    "GITHUB_RUN_ID": RUN,
    "GITHUB_RUN_ATTEMPT": ATTEMPT,
    "GH_TOKEN": "test-github-only",
    "SHIFTBOX_QUALIFICATION_TAILNET_ID": TAILNET_ID,
    "SHIFTBOX_QUALIFICATION_TAILNET_CLIENT_ID": "test-client-id",
    "SHIFTBOX_QUALIFICATION_TAILNET_CLIENT_SECRET": "test-client-secret",
}


def device(hostname=HOSTNAME, tags=None, node_id=NODE):
    return {
        "nodeId": node_id,
        "hostname": hostname,
        "tags": [TAG] if tags is None else tags,
        "created": "2026-11-22T00:10:00Z",
        "isExternal": False,
    }


def run_status(status="completed", attempt=2, **overrides):
    return {
        "id": int(RUN),
        "path": ".github/workflows/qualify-aws.yml@refs/heads/main",
        "event": "workflow_dispatch",
        "head_branch": "main",
        "head_repository": {"full_name": "jflatten/ShiftBox"},
        "actor": {"login": "jflatten"},
        "triggering_actor": {"login": "jflatten"},
        "status": status,
        "conclusion": "failure" if status == "completed" else None,
        "run_attempt": attempt,
        "run_started_at": "2026-11-22T00:04:00Z",
        "updated_at": "2026-11-22T00:30:00Z",
        **overrides,
    }


class Fixture:
    def __init__(self, devices):
        self.devices = copy.deepcopy(devices)
        self.requests = []
        self.deleted = []
        self.oauth = None
        self.latest = run_status()
        self.attempt = run_status()
        self.job_status = "completed"
        self.readback = None
        self.stay_after_delete = False

    def request(self, host, method, path, headers=None, body=None, missing_ok=False):
        self.requests.append((host, method, path))
        if host == "api.tailscale.com":
            if path == "/api/v2/oauth/token" and method == "POST":
                self.oauth = parse_qs(body.decode("ascii"), strict_parsing=True)
                return {"access_token": "test-tailnet-only", "token_type": "Bearer"}
            assert headers["Authorization"] == "Bearer test-tailnet-only"
            assert not body
            if path == f"/api/v2/tailnet/{TAILNET_ID}/devices" and method == "GET":
                return {"devices": copy.deepcopy(self.devices)}
            if path == f"/api/v2/device/{NODE}" and method == "GET":
                return copy.deepcopy(self.readback if self.readback is not None else self.devices[0])
            if path == f"/api/v2/device/{NODE}" and method == "DELETE":
                self.deleted.append(NODE)
                if not self.stay_after_delete:
                    self.devices = [item for item in self.devices if item["nodeId"] != NODE]
                return None
        if host == "api.github.com" and method == "GET":
            assert headers["Authorization"] == "Bearer test-github-only"
            endpoint = f"/repos/jflatten/ShiftBox/actions/runs/{RUN}"
            if path == endpoint:
                return copy.deepcopy(self.latest)
            if path == f"{endpoint}/attempts/{ATTEMPT}":
                return copy.deepcopy(self.attempt)
            if path == f"{endpoint}/attempts/{ATTEMPT}/jobs?per_page=100":
                return {"total_count": 1, "jobs": [{
                    "name": "qualify", "run_id": int(RUN), "head_branch": "main", "status": self.job_status,
                    "conclusion": "failure" if self.job_status == "completed" else None
                }]}
        raise AssertionError(f"Unexpected fixture request: {method} {host}{path}")


def execute(operation, fixture, **env):
    defaults = ENV | {"GITHUB_JOB": "cleanup" if operation == "run" else "sweep"}
    return tailnet.execute(operation, defaults | env, fixture.request, NOW)


class TailnetTests(unittest.TestCase):
    def test_immediate_cleanup_deletes_only_current_run_router(self):
        """A completed deploy job permits deleting just its exact tagged router."""
        fixture = Fixture([device(), device("sbq-456-1-router", node_id="nAnother"), device("laptop", [], "nLaptop")])
        fixture.latest = run_status(status="in_progress")
        fixture.attempt = run_status(status="in_progress")
        execute("run", fixture)
        self.assertEqual(fixture.deleted, [NODE])
        self.assertEqual(len(fixture.devices), 2)
        self.assertEqual(fixture.oauth["scope"], ["devices:core"])
        self.assertEqual(fixture.oauth["tags"], [TAG])

    def test_immediate_cleanup_cannot_delete_before_deploy_job_finishes(self):
        """An accidental early invocation cannot interrupt the active qualification job."""
        fixture = Fixture([device()])
        fixture.latest = run_status(status="in_progress")
        fixture.attempt = run_status(status="in_progress")
        fixture.job_status = "in_progress"
        with self.assertRaises(tailnet.TailnetError):
            execute("run", fixture)
        self.assertEqual(fixture.deleted, [])

    def test_empty_inventory_is_read_only_and_blocks_a_remnant(self):
        """Admission requests read-only scope and fails if any qualification-shaped node remains."""
        fixture = Fixture([device(tags=["tag:foreign"])])
        with self.assertRaises(tailnet.TailnetError):
            execute("empty", fixture)
        self.assertEqual(fixture.oauth["scope"], ["devices:core:read"])
        self.assertEqual(fixture.deleted, [])
        fixture = Fixture([device("laptop", [], "nLaptop")])
        execute("empty", fixture)
        self.assertEqual(fixture.deleted, [])

    def test_sweep_waits_for_completed_attempt_and_older_than_grace(self):
        """An expired completed attempt is eligible, while live retries and fresh completions are not."""
        fixture = Fixture([device()])
        execute("sweep", fixture, GITHUB_EVENT_NAME="schedule", GITHUB_ACTOR="github-actions[bot]")
        self.assertEqual(fixture.deleted, [NODE])

        for latest, attempt in (
            (run_status(status="in_progress", attempt=3), run_status()),
            (run_status(), run_status(updated_at="2026-11-22T00:50:00Z")),
            (run_status(), run_status(status="in_progress")),
        ):
            with self.subTest(latest=latest["status"], attempt=attempt["status"], when=attempt["updated_at"]):
                fixture = Fixture([device()])
                fixture.latest, fixture.attempt = latest, attempt
                execute("sweep", fixture)
                self.assertEqual(fixture.deleted, [])

    def test_sweep_rejects_wrong_workflow_without_deletion(self):
        """The run number alone cannot authorize the router of another GitHub workflow."""
        fixture = Fixture([device()])
        fixture.attempt = run_status(path=".github/workflows/ci.yml")
        with self.assertRaises(tailnet.TailnetError):
            execute("sweep", fixture)
        self.assertEqual(fixture.deleted, [])

    def test_foreign_ambiguous_or_renamed_nodes_are_never_deleted(self):
        """Reject duplicate names, extra tags, externally shared nodes, and changed live metadata."""
        for inventory, readback in (
            ([device(), device(node_id="nAmbiguous")], None),
            ([device(), device("laptop", [])], None),
            ([device(tags=[TAG, "tag:foreign"])], None),
            ([device() | {"isExternal": True}], None),
            ([device() | {"created": "2026-11-21T02:00:00Z"}], None),
            ([device()], device("renamed")),
            ([device()], device(tags=["tag:foreign"])),
        ):
            with self.subTest(inventory=inventory, readback=readback):
                fixture = Fixture(inventory)
                fixture.readback = readback
                with self.assertRaises(tailnet.TailnetError):
                    execute("sweep", fixture)
                self.assertEqual(fixture.deleted, [])

    def test_immediate_cleanup_rejects_other_source_and_wrong_tailnet_alias(self):
        """Wrong repositories, jobs, or an implicit tailnet fail before credential exchange."""
        for overrides in (
            {"GITHUB_WORKFLOW_REF": "other/repo/.github/workflows/qualify-aws.yml@refs/heads/main"},
            {"GITHUB_JOB": "qualify"},
            {"SHIFTBOX_QUALIFICATION_TAILNET_ID": "-"},
        ):
            with self.subTest(overrides=overrides):
                fixture = Fixture([device()])
                with self.assertRaises(tailnet.TailnetError):
                    execute("run", fixture, **overrides)
                self.assertEqual(fixture.requests, [])

    def test_successful_delete_alone_does_not_prove_absence(self):
        """A router still present on the next inventory keeps cleanup incomplete."""
        fixture = Fixture([device()])
        fixture.stay_after_delete = True
        with self.assertRaises(tailnet.TailnetError):
            execute("sweep", fixture)
        self.assertEqual(fixture.deleted, [NODE])

    def test_transport_does_not_follow_redirect_or_cross_origins(self):
        """A redirect never forwards auth, and HTTPS hosts are fixed by the helper."""
        with mock.patch.object(tailnet.http.client, "HTTPSConnection") as https:
            connection = https.return_value
            response = connection.getresponse.return_value
            response.status = 302
            response.read.return_value = json.dumps({"secretEcho": "do not log"}).encode()
            with self.assertRaisesRegex(tailnet.TailnetError, "^api.tailscale.com returned HTTP 302$"):
                tailnet.request("api.tailscale.com", "POST", "/api/v2/oauth/token", body=b"opaque-fixture")
            with self.assertRaises(tailnet.TailnetError):
                tailnet.request("external.example", "GET", "/")
            https.assert_called_once()
            self.assertEqual(connection.request.call_count, 1)


if __name__ == "__main__":
    unittest.main()
