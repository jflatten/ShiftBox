#!/usr/bin/env python3
"""Inspect and remove explicitly owned qualification routers in the fixture tailnet."""

import datetime
import http.client
import json
import os
import re
import sys
from urllib.parse import quote, urlencode


REPO = "jflatten/ShiftBox"
WORKFLOW = ".github/workflows/qualify-aws.yml"
TAILSCALE_HOST = "api.tailscale.com"
GITHUB_HOST = "api.github.com"
ROUTER_TAG = "tag:shiftbox-qualification"
RUN_ID = r"[1-9][0-9]{0,15}"
ATTEMPT = r"[1-9][0-9]{0,3}"
ROUTER = re.compile(rf"sbq-({RUN_ID})-({ATTEMPT})-router")
NODE_ID = re.compile(r"[a-zA-Z0-9_-]{2,128}")
TAILNET_ID = re.compile(r"[a-zA-Z0-9][a-zA-Z0-9._-]{1,127}")
GRACE = datetime.timedelta(minutes=30)
MAX_BODY = 2 * 1024 * 1024


class TailnetError(Exception):
    """A cleanup precondition or remote verification failed."""


def request(host, method, path, headers=None, body=None, missing_ok=False):
    # HTTPSConnection verifies certificates and hostnames by default, ignores
    # proxy environment variables, and does not follow HTTP redirects.
    if host not in {TAILSCALE_HOST, GITHUB_HOST} or not path.startswith("/") or path.startswith("//"):
        raise TailnetError("unexpected API destination")
    if host == GITHUB_HOST and method != "GET":
        raise TailnetError("GitHub access is read-only")
    connection = http.client.HTTPSConnection(host, timeout=10)
    try:
        connection.request(method, path, body=body, headers=headers or {})
        response = connection.getresponse()
        if (response.status == 404 and missing_ok) or (response.status == 204 and method == "DELETE"):
            return None
        if response.status != 200:
            raise TailnetError(f"{host} returned HTTP {response.status}")
        if method == "DELETE":
            return None
        payload = response.read(MAX_BODY + 1)
        if len(payload) > MAX_BODY:
            raise TailnetError(f"{host} returned an oversized response")
        return json.loads(payload)
    except (OSError, http.client.HTTPException, ValueError):
        raise TailnetError(f"could not verify {host} API response") from None
    finally:
        connection.close()


def configured(env, key):
    value = env.get(key, "")
    if not re.fullmatch(r"[!-~]{1,4096}", value):
        raise TailnetError(f"missing or malformed {key}")
    return value


def check_workflow(operation, env):
    if (
        env.get("GITHUB_REPOSITORY") != REPO
        or env.get("GITHUB_REF") != "refs/heads/main"
        or env.get("GITHUB_WORKFLOW_REF") != f"{REPO}/{WORKFLOW}@refs/heads/main"
        or env.get("GITHUB_JOB") != ("cleanup" if operation == "run" else "sweep")
    ):
        raise TailnetError("requires the protected qualification cleanup workflow on main")
    if operation == "sweep" and env.get("GITHUB_EVENT_NAME") == "schedule":
        return
    if (
        env.get("GITHUB_EVENT_NAME") != "workflow_dispatch"
        or env.get("GITHUB_ACTOR") != "jflatten"
        or env.get("GITHUB_TRIGGERING_ACTOR") != "jflatten"
    ):
        raise TailnetError("requires the approved maintainer's qualification dispatch")


def clients(operation, env, api):
    tailnet_id = configured(env, "SHIFTBOX_QUALIFICATION_TAILNET_ID")
    if not TAILNET_ID.fullmatch(tailnet_id):
        raise TailnetError("configure the exact dedicated fixture tailnet ID")
    github_token = configured(env, "GH_TOKEN") if operation != "empty" else None
    scope = "devices:core:read" if operation == "empty" else "devices:core"
    token = api(
        TAILSCALE_HOST,
        "POST",
        "/api/v2/oauth/token",
        {"Content-Type": "application/x-www-form-urlencoded"},
        urlencode(
            {
                "grant_type": "client_credentials",
                "client_id": configured(env, "SHIFTBOX_QUALIFICATION_TAILNET_CLIENT_ID"),
                "client_secret": configured(env, "SHIFTBOX_QUALIFICATION_TAILNET_CLIENT_SECRET"),
                "scope": scope,
                "tags": ROUTER_TAG,
            }
        ).encode("ascii"),
    )
    token_type = token.get("token_type") if isinstance(token, dict) else None
    access_token = token.get("access_token") if isinstance(token, dict) else None
    if (
        not isinstance(token_type, str)
        or token_type.lower() != "bearer"
        or not isinstance(access_token, str)
        or not re.fullmatch(r"[!-~]{1,4096}", access_token)
        or token.get("scope", scope) != scope
    ):
        raise TailnetError("could not verify the scoped Tailscale OAuth token")

    def tailscale(method, path, missing_ok=False):
        if operation == "empty" and method != "GET":
            raise TailnetError("empty inventory check is read-only")
        return api(TAILSCALE_HOST, method, path, {"Authorization": f"Bearer {access_token}"}, missing_ok=missing_ok)

    def github(path):
        return api(
            GITHUB_HOST,
            "GET",
            f"/repos/{REPO}/actions/runs/{path}",
            {"Authorization": f"Bearer {github_token}", "Accept": "application/vnd.github+json",
             "X-GitHub-Api-Version": "2026-03-10"},
        )

    return tailscale, github, f"/api/v2/tailnet/{quote(tailnet_id, safe='')}/devices"


def timestamp(value):
    try:
        result = datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except (AttributeError, ValueError):
        raise TailnetError("could not verify a run or router timestamp") from None
    if result.tzinfo is None:
        raise TailnetError("run or router timestamp is missing a timezone")
    return result


def lookup_run(api, run, attempt=None):
    path = run if attempt is None else f"{run}/attempts/{attempt}"
    info = api(path)
    if (
        not isinstance(info, dict)
        or type(info.get("id")) is not int
        or str(info["id"]) != run
        or info.get("path") not in (WORKFLOW, f"{WORKFLOW}@main", f"{WORKFLOW}@refs/heads/main")
        or info.get("event") != "workflow_dispatch"
        or info.get("head_branch") != "main"
        or not isinstance(info.get("head_repository"), dict)
        or info["head_repository"].get("full_name") != REPO
        or not isinstance(info.get("actor"), dict)
        or info["actor"].get("login") != "jflatten"
        or not isinstance(info.get("triggering_actor"), dict)
        or info["triggering_actor"].get("login") != "jflatten"
        or not isinstance(info.get("status"), str)
        or type(info.get("run_attempt")) is not int
        or (attempt is not None and str(info["run_attempt"]) != attempt)
    ):
        raise TailnetError("could not verify the qualification run identity")
    return info


def completed_deploy_job(api, run, attempt):
    result = api(f"{run}/attempts/{attempt}/jobs?per_page=100")
    if not isinstance(result, dict) or not isinstance(result.get("jobs"), list):
        raise TailnetError("could not verify qualification job state")
    jobs = result["jobs"]
    if len(jobs) != result.get("total_count") or any(not isinstance(job, dict) for job in jobs):
        raise TailnetError("qualification job inventory is incomplete")
    qualify = [job for job in jobs if job.get("name") == "qualify"]
    if (
        len(qualify) != 1
        or qualify[0].get("status") != "completed"
        or qualify[0].get("conclusion") not in ("success", "failure", "cancelled", "timed_out")
        or str(qualify[0].get("run_id")) != run
        or qualify[0].get("head_branch") != "main"
    ):
        raise TailnetError("qualification deploy job has not finished")


def qualification_devices(api, path):
    response = api("GET", path)
    if (
        not isinstance(response, dict)
        or not isinstance(response.get("devices"), list)
        or any(response.get(field) for field in ("cursor", "next_cursor", "nextCursor"))
    ):
        raise TailnetError("fixture tailnet returned incomplete device inventory")
    devices = response["devices"]
    if any(
        not isinstance(device, dict)
        or not isinstance(device.get("hostname"), str)
        or (device.get("tags") is not None and not isinstance(device["tags"], list))
        for device in devices
    ):
        raise TailnetError("could not verify fixture device inventory")
    node_ids = [device["nodeId"] for device in devices if isinstance(device.get("nodeId"), str)]
    if len(set(node_ids)) != len(node_ids):
        raise TailnetError("fixture router ownership is ambiguous")
    return [
        device for device in devices if ROUTER.fullmatch(device["hostname"]) or ROUTER_TAG in (device.get("tags") or [])
    ]


def router_identity(row):
    if not isinstance(row, dict):
        raise TailnetError("could not verify the qualification router")
    hostname = row.get("hostname")
    node = row.get("nodeId")
    if (
        not isinstance(hostname, str)
        or not ROUTER.fullmatch(hostname)
        or row.get("tags") != [ROUTER_TAG]
        or not isinstance(node, str)
        or not NODE_ID.fullmatch(node)
        or row.get("isExternal") is True
    ):
        raise TailnetError("refusing an unverified or foreign fixture router")
    return hostname, node, timestamp(row.get("created"))


def remove_router(api, listing, row, started, ended):
    hostname, node, created = router_identity(row)
    if not started <= created <= ended:
        raise TailnetError("the router was created outside its verified run attempt")
    path = f"/api/v2/device/{quote(node, safe='')}"
    current = api("GET", path, missing_ok=True)
    if current is not None:
        if router_identity(current) != (hostname, node, created):
            raise TailnetError("qualification router identity changed before deletion")
        api("DELETE", path, missing_ok=True)
    # An ephemeral node may disappear between reads. Confirm absence regardless
    # of whether the DELETE endpoint or automatic expiry removed it.
    if any(
        device.get("nodeId") == node or device["hostname"] == hostname
        for device in qualification_devices(api, listing)
    ):
        raise TailnetError("fixture router still present after cleanup")


def execute(operation, env, api, now):
    if operation not in {"run", "sweep", "empty"}:
        raise TailnetError("usage: tailnet.py run|sweep|empty")
    check_workflow(operation, env)
    current_run = env.get("GITHUB_RUN_ID", "")
    current_attempt = env.get("GITHUB_RUN_ATTEMPT", "")
    if not re.fullmatch(RUN_ID, current_run) or not re.fullmatch(ATTEMPT, current_attempt):
        raise TailnetError("invalid current workflow run identity")
    tailscale, github, listing = clients(operation, env, api)
    devices = qualification_devices(tailscale, listing)
    if operation == "empty":
        if devices:
            raise TailnetError("qualification routers remain in the fixture tailnet")
        return
    if operation == "run":
        target = f"sbq-{current_run}-{current_attempt}-router"
        devices = [device for device in devices if device["hostname"] == target]
    targets = [router_identity(device) for device in devices]
    if len({hostname for hostname, _, _ in targets}) != len(targets):
        raise TailnetError("fixture router ownership is ambiguous")
    for device, (hostname, _, _) in zip(devices, targets):
        run, attempt = ROUTER.fullmatch(hostname).groups()
        latest = lookup_run(github, run)
        if operation == "sweep" and latest["status"] != "completed":
            # A queued or active retry prevents independent cleanup.
            continue
        if latest["run_attempt"] < int(attempt):
            raise TailnetError("router names a workflow attempt that does not exist")
        record = lookup_run(github, run, attempt)
        started = timestamp(record.get("run_started_at"))
        if operation == "run":
            if latest["run_attempt"] != int(current_attempt):
                raise TailnetError("per-run cleanup requires the latest workflow attempt")
            completed_deploy_job(github, run, attempt)
            remove_router(tailscale, listing, device, started, now)
        elif record["status"] == "completed" and isinstance(record.get("conclusion"), str):
            ended = timestamp(record.get("updated_at"))
            if now - ended >= GRACE:
                remove_router(tailscale, listing, device, started, ended)


def main():
    try:
        if len(sys.argv) != 2:
            raise TailnetError("usage: tailnet.py run|sweep|empty")
        execute(sys.argv[1], os.environ, request, datetime.datetime.now(datetime.timezone.utc))
    except TailnetError as error:
        print(f"ShiftBox qualification tailnet: {error}", file=sys.stderr)
        return 1
    print("Fixture tailnet qualification check completed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
