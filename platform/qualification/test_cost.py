"""Fixture checks for the offline qualification estimate and budget gate."""

import json
import subprocess
import sys
from decimal import Decimal
from pathlib import Path

import pytest


CALCULATOR = Path(__file__).with_name("cost.py")


def run_calculator(*args):
    return subprocess.run(
        [sys.executable, str(CALCULATOR), *args],
        capture_output=True,
        check=False,
        text=True,
    )


def test_estimate_reports_the_full_and_automated_scenarios_separately():
    # One foundation validation is cheaper than a staffed workspace/recovery
    # session and must not be reported as the full qualification estimate.
    result = run_calculator()
    assert result.returncode == 0, result.stderr
    report = json.loads(result.stdout)
    full = report["qualification"]
    automated = full["automated_only"]
    assert Decimal(full["estimate_usd"]) > Decimal(automated["estimate_usd"])
    assert Decimal(full["planning_ceiling_usd"]) > Decimal(full["estimate_usd"])
    assert full["breakdown_usd"]["private_dns_hosted_zones"] == "0.00"
    assert full["ceiling_breakdown_usd"]["private_dns_hosted_zones"] == "1.00"
    assert list(full["regional_probes_usd"]) == [
        "us-east-1",
        "us-east-2",
        "us-west-1",
        "us-west-2",
    ]
    assert (
        Decimal(full["regional_probes_usd"]["us-west-1"])
        > Decimal(full["regional_probes_usd"]["us-east-2"])
    )
    assert report["budget"]["headroom_usd"] is None
    assert report["budget"]["meets_planning_ceiling"] is None
    assert Decimal(report["publisher"]["retention_per_month_usd"]) > 0


def test_insufficient_headroom_fails_closed_with_the_cost_report():
    # A maintainer-supplied balance below the buffered estimate must not
    # authorize a run, even when the expected bill might otherwise fit.
    report = json.loads(run_calculator().stdout)
    ceiling = Decimal(report["qualification"]["planning_ceiling_usd"])
    denied = run_calculator("--headroom-usd", str(ceiling - Decimal("0.01")))
    assert denied.returncode == 1
    assert json.loads(denied.stdout)["budget"]["meets_planning_ceiling"] is False


def test_exact_planning_ceiling_meets_the_headroom_check():
    # The boundary admits an explicitly supplied amount equal to the ceiling.
    ceiling = json.loads(run_calculator().stdout)["qualification"]["planning_ceiling_usd"]
    approved = run_calculator("--headroom-usd", ceiling)
    assert approved.returncode == 0
    assert json.loads(approved.stdout)["budget"]["meets_planning_ceiling"] is True


def test_automated_admission_uses_only_the_automated_ceiling():
    # Partial scope can fit below the staffed ceiling, and must be explicit.
    report = json.loads(run_calculator().stdout)
    ceiling = report["qualification"]["automated_only"]["planning_ceiling_usd"]
    approved = run_calculator("--mode", "automated", "--headroom-usd", ceiling)
    assert approved.returncode == 0, approved.stderr
    assert json.loads(approved.stdout)["budget"]["mode"] == "automated"
    assert run_calculator("--mode", "staffed", "--headroom-usd", ceiling).returncode == 1


@pytest.mark.parametrize("balance", ["NaN", "Infinity", "-0.01", "30.01", "5e0", "1.001"])
def test_invalid_or_over_cap_headroom_is_rejected(balance):
    # Dispatch input must be a finite USD amount within the aggregate budget,
    # with no exponent or fractional cents hiding the comparison boundary.
    result = run_calculator("--headroom-usd", balance)
    assert result.returncode == 2
    assert "headroom" in result.stderr.lower()
