#!/usr/bin/env python3
"""Estimate one isolated ShiftBox qualification run without querying AWS."""

import argparse
import json
import re
from decimal import ROUND_CEILING, Decimal


D = Decimal
BUDGET = D("30")
MONTH_HOURS = D("720")
OHIO = "us-east-2"
OCI_IMAGE_CACHE_GIB = 20
T3_MEDIUM = {
    "us-east-1": D("0.0416"),
    "us-east-2": D("0.0416"),
    "us-west-1": D("0.0496"),
    "us-west-2": D("0.0416"),
}
GP3 = {
    "us-east-1": D("0.080"),
    "us-east-2": D("0.080"),
    "us-west-1": D("0.096"),
    "us-west-2": D("0.080"),
}
SNAPSHOTS = {
    "us-east-1": D("0.050"),
    "us-east-2": D("0.050"),
    "us-west-1": D("0.055"),
    "us-west-2": D("0.050"),
}
S3_STANDARD = {
    "us-east-1": D("0.023"),
    "us-east-2": D("0.023"),
    "us-west-1": D("0.026"),
    "us-west-2": D("0.023"),
}
OHIO_STANDARD_TRANSFER = (D("0.01"), D("0.02"), D("0.02"))
PUBLIC_CATALOG = (
    "https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/"
    "AmazonEC2/20261121004816"
)
RATE_SOURCES = {
    "regional_ec2": [
        f"{PUBLIC_CATALOG}/{region}/index.csv" for region in T3_MEDIUM
    ],
    "ec2_transfer_and_cpu_credits": "https://aws.amazon.com/ec2/pricing/on-demand/",
    "ohio_transfer": (
        "https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/"
        "AWSDataTransfer/20261119105841/us-east-2/index.csv"
    ),
    "nat_and_ipv4": "https://aws.amazon.com/vpc/pricing/",
    "storage_and_snapshots": "https://aws.amazon.com/ebs/pricing/",
    "private_dns": "https://aws.amazon.com/route53/pricing/",
    "service_discovery": "https://aws.amazon.com/cloud-map/pricing/",
    "secrets": "https://aws.amazon.com/secrets-manager/pricing/",
    "ohio_logs": (
        "https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/"
        "AmazonCloudWatch/20261119171040/us-east-2/index.csv"
    ),
    "template_storage": [
        "https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/"
        f"AmazonS3/20261121172919/{region}/index.csv"
        for region in S3_STANDARD
    ],
    "runner": "https://docs.github.com/en/actions/concepts/billing-and-usage",
}


def usd(amount, places="0.01"):
    """Round budgeting figures up to the chosen USD precision."""
    return str(amount.quantize(D(places), rounding=ROUND_CEILING))


def regional_probes(hours):
    """Price isolated probes with a 32 GiB root and no public IP, NAT, or logs."""
    return {
        region: hours * (rate + GP3[region] * 32 / MONTH_HOURS + 2 * D("0.05"))
        for region, rate in T3_MEDIUM.items()
    }


def ohio_stack(staffed, buffered):
    """Use the checked-in topology and price recovery as a scoped private target."""
    hours = D(2 if staffed else 1)
    if buffered:
        hours += 2
    # The recovery target adds one hour of t3.medium and one 32 GiB disk. It
    # may run after the workspace stops to avoid raising the vCPU requirement.
    recovery_hours = D(1 if staffed else 0)
    workspace_hours = hours if staffed else D(0)
    t3_hours = hours + workspace_hours + recovery_hours
    disk_gib_hours = (
        (30 + 32 + OCI_IMAGE_CACHE_GIB) * hours
        + 32 * (workspace_hours + recovery_hours)
    )
    # Charge a worst-case extra day for the retained installation, workspace
    # and recovery volumes if destruction is delayed after stack deletion.
    storage_tail = 24 if buffered else 0
    disk_gib_hours += 32 * (3 if staffed else 1) * storage_tail
    egress_gib = D(20 if buffered else (5 if staffed else 2))
    outbound_gib = D(5 if buffered else 1)
    logs_gib = D("0.50" if buffered else "0.10")
    secret_hours = (hours + storage_tail).to_integral_value(rounding=ROUND_CEILING)
    # Cloud Map's task registrations are priced at a full month in the buffer
    # because the published examples do not specify short-run rounding.
    if buffered and staffed:
        cloud_map = D("0.20")
    else:
        cloud_map = D("0.20") * workspace_hours / MONTH_HOURS
    secret_requests = 6000 if buffered else 1000
    t3_cpu_credits = (2 * t3_hours if buffered else t3_hours) * D("0.05")
    return {
        "ecs_host_ec2": hours * T3_MEDIUM[OHIO],
        "workspace_ec2": workspace_hours * T3_MEDIUM[OHIO],
        "recovery_target_ec2": recovery_hours * T3_MEDIUM[OHIO],
        "gp3_storage_including_retention": disk_gib_hours * GP3[OHIO] / MONTH_HOURS,
        "cpu_credit_allowance": t3_cpu_credits,
        "existing_egress_processing_allowance": egress_gib * D("0.045"),
        "internet_egress_allowance": outbound_gib * D("0.09"),
        "private_dns_hosted_zones": D("1.00" if buffered else "0.00"),
        "cloud_map_registrations_and_calls": cloud_map + D("0.001"),
        "secrets_storage_and_calls": 4 * secret_hours * D("0.40") / MONTH_HOURS
        + secret_requests * D("0.05") / 10000,
        "logs_ingestion_and_storage": logs_gib
        * (D("0.50") + D("0.03") * (hours + storage_tail) / MONTH_HOURS),
    }


def qualification_scenario(staffed):
    """Estimate the run and the independent-sweep contingency separately."""
    probes = regional_probes(D("0.25"))
    ceiling_probes = regional_probes(D("2.25"))
    ohio = ohio_stack(staffed=staffed, buffered=False)
    ceiling_ohio = ohio_stack(staffed=staffed, buffered=True)
    subtotal = sum(probes.values()) + sum(ohio.values())
    buffer_subtotal = sum(ceiling_probes.values()) + sum(ceiling_ohio.values())
    # Round the contingency case up to a whole dollar for further incidental
    # charges. It is a planning threshold, not an enforced AWS spending limit.
    ceiling = buffer_subtotal.to_integral_value(rounding=ROUND_CEILING)
    return {
        "estimate_usd": usd(subtotal),
        "planning_ceiling_usd": usd(ceiling),
        "breakdown_usd": {name: usd(amount) for name, amount in ohio.items()},
        "regional_probes_usd": {
            name: usd(amount, "0.0001") for name, amount in probes.items()
        },
        "ceiling_breakdown_usd": {name: usd(amount) for name, amount in ceiling_ohio.items()},
        "ceiling_probe_total_usd": usd(sum(ceiling_probes.values())),
        "assumptions": {
            "ohio_hours_including_teardown": 2 if staffed else 1,
            "oci_image_cache_gib": OCI_IMAGE_CACHE_GIB,
            "automated_execution_minutes": 45,
            "staffed_operator_minutes": 60 if staffed else 0,
            "teardown_minutes": 15,
            "regional_probe_minutes_each": 15,
            "additional_running_orphan_hours_in_ceiling": 2,
            "additional_retained_disk_and_secret_hours_in_ceiling": 24,
            "existing_egress_processed_gib": 5 if staffed else 2,
            "existing_egress_processed_gib_in_ceiling": 20,
            "internet_egress_gib": 1,
            "internet_egress_gib_in_ceiling": 5,
            "log_ingest_gib": "0.10",
            "log_ingest_gib_in_ceiling": "0.50",
            "recovery_target_hours": 1 if staffed else 0,
        },
    }


def publisher_estimate():
    """Show one new RC's build and durable assets separately from testing."""
    snapshots = 32 * sum(SNAPSHOTS.values())
    # Budget up to 1 MiB of template object storage in each region.
    templates = sum(S3_STANDARD.values()) / 1024
    one_hour_build = T3_MEDIUM[OHIO] + 32 * GP3[OHIO] / MONTH_HOURS + 2 * D("0.05")
    ohio_copy_transfer = 32 * sum(OHIO_STANDARD_TRANSFER)
    # The source AMI is one of the four public AMIs. Allow $0.03/GiB for each
    # of three copies as a reserve when the publisher chooses another region.
    copy_reserve = 3 * 32 * D("0.03")
    return {
        "retention_per_month_usd": usd(snapshots + templates),
        "snapshot_storage_per_month_usd": usd(snapshots),
        "template_storage_per_month_usd": usd(templates, "0.0001"),
        "one_hour_ohio_build_compute_and_disk_usd": usd(one_hour_build),
        "three_copies_standard_ohio_transfer_usd": usd(ohio_copy_transfer),
        "three_regional_copies_transfer_allowance_usd": usd(copy_reserve),
        "one_time_build_and_copy_allowance_usd": usd(one_hour_build + copy_reserve),
        "notes": [
            "Retention assumes one RC and up to 32 GiB billed snapshot data per region.",
            "Stable promotion reuses the qualified RC assets.",
            "Exclude Packer runner/network, failed candidates, retries, and ongoing public S3 downloads.",
        ],
    }


def headroom_amount(value):
    """Accept an explicit dollar amount within the aggregate cap."""
    if re.fullmatch(r"[0-9]{1,2}(?:\.[0-9]{1,2})?", value) and D(value) <= BUDGET:
        return D(value)
    raise argparse.ArgumentTypeError("headroom must be a USD amount between 0 and 30.00")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--headroom-usd", type=headroom_amount)
    parser.add_argument("--mode", choices=("automated", "staffed"), default="staffed")
    args = parser.parse_args()
    qualification = qualification_scenario(staffed=True)
    qualification["automated_only"] = qualification_scenario(staffed=False)
    headroom = args.headroom_usd
    scenario = qualification["automated_only"] if args.mode == "automated" else qualification
    required = scenario["planning_ceiling_usd"]
    admitted = None if headroom is None else headroom >= D(required)
    print(json.dumps({
        "rates_as_of": "2026-11-21",
        "rate_sources": RATE_SOURCES,
        "qualification": qualification,
        "publisher": publisher_estimate(),
        "budget": {
            "aggregate_monthly_cap_usd": usd(BUDGET),
            "mode": args.mode,
            "required_headroom_usd": required,
            "headroom_usd": None if headroom is None else usd(headroom),
            "meets_planning_ceiling": admitted,
            "headroom_source": "unavailable" if headroom is None else "maintainer input",
        },
    }, indent=2))
    if admitted is False:
        parser.exit(1, "Supplied aggregate headroom is below the selected planning ceiling.\n")


if __name__ == "__main__":
    main()
