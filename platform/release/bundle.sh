#!/usr/bin/env bash
# Renders the standalone template and packages the public files from locally staged pins.
set -euo pipefail

if [ "$#" -ne 4 ]; then
  printf 'Usage: bundle.sh <pins.json> <catalog-directory> <public-guide-directory> <new-output-directory>\n' >&2
  exit 2
fi

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=$(cd -- "$here/../.." && pwd)
# shellcheck source=platform/release/manifest.sh
source "$here/manifest.sh"

pins=$1
catalog_dir=$2
guide_dir=$3
out=$4

# Use the same public contract predicates consumed by manifest_validate.
jq -es "$manifest_filter"'
  length == 1 and (.[0] as $release | $release |
    shape(["schemaVersion", "candidateTag", "sourceCommit", "runtimeCompatibility",
      "publisherAccountId", "controller", "postgresImage", "regions"]) and
    .schemaVersion == 1 and (.candidateTag | candidate) and (.sourceCommit | commit) and
    .runtimeCompatibility == .candidateTag and (.publisherAccountId | matches("^[0-9]{12}")) and
    (.controller | shape(["repository", "digest", "platform"]) and
      .repository == "ghcr.io/jflatten/shiftbox" and .platform == "linux/amd64" and
      (.digest | matches("^sha256:[0-9a-f]{64}"))) and
    (.postgresImage | matches("^docker[.]io/library/postgres:17-(bookworm|bullseye|trixie)@sha256:[0-9a-f]{64}")) and
    (.regions | shape(supported_regions) and all(to_entries[];
      .key as $region | .value | shape(["hostAmiId", "workspaceImage"]) and
      (.hostAmiId | ami) and (.workspaceImage | workspace($release; $region) and
        .rootDeviceName == "/dev/sda1" and .minimumDiskGiB == 32 and
        (.sourceRegion as $source | supported_regions | index($source) != null)))))
' <"$pins" >/dev/null || manifest_fail 'candidate pins are incomplete or inconsistent'

python3 - "$root" "$pins" "$catalog_dir" "$guide_dir" "$out" <<'PY'
from gzip import GzipFile
import hashlib
from io import BytesIO
import json
from pathlib import Path
import re
import runpy
import sys
import tarfile
import yaml

root, pins, catalog_dir, guide_dir, out = map(Path, sys.argv[1:])
release = json.loads(pins.read_text())
cloudformation = root / "platform/cloudformation/aws"
regions = ("us-east-1", "us-east-2", "us-west-1", "us-west-2")
guides = ("configure", "contributing", "first-workspace", "install", "private-access", "setup-helper", "troubleshooting", "upgrade-recovery")


def read_regular(path):
    """Prevent links from drawing private or unexpected content into a public archive."""
    if path.is_symlink() or not path.is_file():
        raise ValueError(f"Expected a regular release input: {path.name}")
    return path.read_bytes()


def catalog_bytes(region):
    """Check the frozen Packer bytes bind this region to the exact release and source."""
    image = release["regions"][region]["workspaceImage"]
    contents = read_regular(catalog_dir / f"{region}.json")
    if hashlib.sha256(contents).hexdigest() != image["catalogSha256"]:
        raise ValueError(f"{region}: catalog checksum does not match the staged pin")
    catalog = json.loads(contents)
    builds = catalog["builds"]
    if len(builds) != 1:
        raise ValueError(f"{region}: expected one catalog build")
    build = builds[0]
    metadata = build["custom_data"]
    expected = {
        "provider": "ec2",
        "region": region,
        "releaseId": release["candidateTag"],
        "guestAgentRelease": image["guestRelease"],
        "sourceRevision": image["sourceCommit"],
        "sourceAmiId": image["sourceAmiId"],
        "sourceRegion": image["sourceRegion"],
        "sourceAmiOwnerId": "099720109477",
        "architecture": image["architecture"],
        "minimumRootDiskGiB": str(image["minimumDiskGiB"]),
        "platformVersion": "24.04",
        "protocolVersion": "1",
    }
    digests = {
        "guestAgentSHA256", "guestUpdateKeySHA256", "toolchainRecipeSHA256", "codeServerRecipeSHA256"
    }
    if (
        build["builder_type"] != "amazon-ebs"
        or build["artifact_id"] != f"{region}:{image['amiId']}"
        or build["packer_run_uuid"] != catalog["last_run_uuid"]
        or not re.fullmatch(r"[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}", catalog["last_run_uuid"])
        or set(metadata) != set(expected) | digests
        or any(metadata[key] != value for key, value in expected.items())
        or any(not re.fullmatch(r"[0-9a-f]{64}", metadata[key]) for key in digests)
    ):
        raise ValueError(f"{region}: catalog does not match the staged public image provenance")
    return contents


def render_template():
    """Use the same pins in the template, archive provenance and the distribution manifest."""
    mappings = {}
    for region in regions:
        data = release["regions"][region]
        image = data["workspaceImage"]
        mappings[region] = {
            "HostAmi": data["hostAmiId"],
            "WorkspaceAmi": image["amiId"],
            "PublisherAccount": release["publisherAccountId"],
            "WorkspaceRootDevice": image["rootDeviceName"],
            "WorkspaceSnapshot": image["rootSnapshotId"],
            "WorkspaceSourceAmi": image["sourceAmiId"],
            "WorkspaceSourceRegion": image["sourceRegion"],
            "WorkspaceSourceRevision": image["sourceCommit"],
            "WorkspaceRelease": release["candidateTag"],
            "GuestRelease": image["guestRelease"],
            "WorkspaceArtifact": "workspace-ami",
            "WorkspaceArchitecture": image["architecture"],
            "WorkspaceCatalogSHA256": image["catalogSha256"],
            "WorkspaceMinimumDiskGiB": str(image["minimumDiskGiB"]),
        }
    values = {
        "RELEASE_REGIONS": mappings,
        "CONTROLLER_IMAGE": f"{release['controller']['repository']}@{release['controller']['digest']}",
        "POSTGRES_IMAGE": release["postgresImage"],
        "MOUNT_DATA_SCRIPT": read_regular(cloudformation / "host/mount-data.sh").decode(),
        "MOUNT_CACHE_SCRIPT": read_regular(cloudformation / "host/mount-registry-cache.sh").decode(),
        "POSTGRES_START_SCRIPT": read_regular(cloudformation / "postgres/start.sh").decode(),
        "INSTALLATION_TLS_SCRIPT": read_regular(cloudformation / "host/installation-tls.sh").decode(),
    }
    source = read_regular(cloudformation / "template.yaml").decode()
    boundary_renderer = runpy.run_path(str(cloudformation / "render_boundaries.py"))["render_task_boundaries"]
    template = boundary_renderer(source)
    matches = re.findall(r"@@([A-Z_]+)@@", template)
    if set(matches) != set(values) or matches.count("CONTROLLER_IMAGE") != 2:
        raise ValueError("CloudFormation source markers do not match the renderer")
    rendered = re.sub(
        r"@@([A-Z_]+)@@", lambda match: json.dumps(values[match[1]], sort_keys=True), template
    )
    encoded = rendered.encode()
    if len(encoded) > 1_048_576:
        raise ValueError("Rendered template exceeds the CloudFormation S3 template size limit")
    return encoded


def build_installer():
    """Embed the approved publisher and validators in one fully parsed Bash block."""
    validator = read_regular(root / "platform/release/manifest.sh").decode()
    start = validator.index("\n") + 1
    end = validator.index('\nif [ "${BASH_SOURCE[0]}" = "$0" ]; then\n')
    main = read_regular(root / "scripts/install-aws.sh").decode()
    main = main[main.index("\n") + 1:]
    return (
        "#!/usr/bin/env bash\n"
        "{\n"
        f"shiftbox_release_publisher='{release['publisherAccountId']}'\n"
        f"{validator[start:end]}\n"
        f"{main.rstrip()}\n"
        "}\n"
    ).encode()


def build_support_archive(destination, catalog, template):
    """Archive only public files, with stable ordering, ownership, permissions and time."""
    members = {f"catalog/{region}.json": (contents, 0o644) for region, contents in catalog.items()}
    members.update({
        f"docs/{guide}.md": (read_regular(guide_dir / f"{guide}.md"), 0o644) for guide in guides
    })
    members["setup.sh"] = (read_regular(cloudformation / "setup.sh"), 0o755)
    members["stack-policy.json"] = (read_regular(cloudformation / "stack-policy.json"), 0o644)
    launch_forms = runpy.run_path(str(cloudformation / "launch_forms.py"))
    base = yaml.load(template, Loader=launch_forms["CloudFormationLoader"])
    for mode in launch_forms["FORMS"]:
        form = launch_forms["configure_launch_form"](base, mode)
        form_bytes = (json.dumps(form, indent=2) + "\n").encode()
        if len(form_bytes) > 1_048_576:
            raise ValueError("Console launch form exceeds the CloudFormation S3 template size limit")
        members[f"launch/shiftbox-aws-{mode}.json"] = (form_bytes, 0o644)
    with destination.open("xb") as raw, GzipFile(filename="", fileobj=raw, mode="wb", mtime=0) as archive:
        with tarfile.open(fileobj=archive, mode="w", format=tarfile.USTAR_FORMAT) as tar:
            for name, (data, mode) in sorted(members.items()):
                entry = tarfile.TarInfo(name)
                entry.size = len(data)
                entry.mode = mode
                tar.addfile(entry, BytesIO(data))


try:
    catalog = {region: catalog_bytes(region) for region in regions}
    template = render_template()
    installer = build_installer()
    out.mkdir()
    (out / "shiftbox-aws-cloudformation.yaml").write_bytes(template)
    install_path = out / "install.sh"
    install_path.write_bytes(installer)
    install_path.chmod(0o755)
    build_support_archive(out / "shiftbox-aws-support.tar.gz", catalog, template)
except (KeyError, OSError, TypeError, ValueError) as error:
    sys.exit(f"ShiftBox release: {error}")
PY

bash -n "$out/install.sh"
manifest_validate_support "$out/shiftbox-aws-support.tar.gz"
