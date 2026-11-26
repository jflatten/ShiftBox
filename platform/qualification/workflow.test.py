#!/usr/bin/env python3
"""Check workflow authority and teardown wiring without contacting AWS."""
from pathlib import Path
import unittest

import yaml


class QualificationWorkflow(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = Path(__file__).resolve().parents[2] / ".github/workflows/qualify-aws.yml"
        cls.workflow = yaml.load(path.read_text(), Loader=yaml.BaseLoader)

    def test_events_and_single_lock(self):
        """A PR or branch push must never acquire AWS credentials."""
        self.assertEqual({"workflow_dispatch", "schedule"}, self.workflow["on"].keys())
        self.assertEqual("shiftbox-aws-qualification", self.workflow["concurrency"]["group"])
        self.assertEqual("false", self.workflow["concurrency"]["cancel-in-progress"])

    def test_admission_runs_before_any_deployment_credentials(self):
        """Explicit cost acceptance and artifact checks must precede deploy access."""
        jobs = self.workflow["jobs"]
        self.assertNotIn("id-token", jobs["admission"]["permissions"])
        admission = str(jobs["admission"]["steps"])
        self.assertIn("--headroom-usd", admission)
        self.assertIn("SHIFTBOX_QUALIFICATION_ENABLED", admission)
        self.assertIn("admission", jobs["qualify"]["needs"])
        steps = jobs["qualify"]["steps"]
        fetch = next(index for index, step in enumerate(steps) if "preflight.sh fetch" in step.get("run", ""))
        token = next(index for index, step in enumerate(steps) if "configure-aws-credentials" in step.get("uses", ""))
        self.assertLess(fetch, token)
        self.assertIn("allowed-account-ids", steps[token]["with"])

    def test_cleanup_independent_of_qualification_runner(self):
        """Sweeps and immediate cleanup use cleanup-only credentials and named bounds."""
        jobs = self.workflow["jobs"]
        self.assertIn("schedule", jobs["sweep"]["if"])
        self.assertIn("github.ref == 'refs/heads/main'", jobs["sweep"]["if"])
        self.assertNotIn("needs", jobs["sweep"]["if"].split("github.event_name == 'schedule'")[0])
        for job in (jobs["sweep"], jobs["cleanup"]):
            self.assertEqual("aws-qualification-cleanup", job["environment"])
            self.assertIn("SHIFTBOX_QUALIFICATION_CLEANUP_ROLE_ARN", str(job["steps"]))
            self.assertNotIn("SHIFTBOX_QUALIFICATION_DEPLOY_ROLE_ARN", str(job["steps"]))
        self.assertIn("always()", jobs["cleanup"]["if"])
        self.assertIn("cleanup.sh run", str(jobs["cleanup"]["steps"]))
        self.assertIn("cleanup.sh sweep", str(jobs["sweep"]["steps"]))
        self.assertIn("cleanup.sh empty", str(jobs["sweep"]["steps"]))

    def test_named_qualification_and_staffed_handoff(self):
        """The workflow points the operator at the verified template and reserves execution for the staffed path."""
        qualify = self.workflow["jobs"]["qualify"]
        self.assertIn("vars.SHIFTBOX_QUALIFICATION_ENABLED == 'true'", qualify["if"])
        self.assertIn("vars.SHIFTBOX_QUALIFICATION_SWEEP_ENABLED == 'true'", qualify["if"])
        deploy = next(s for s in qualify["steps"] if "deploy.sh" in s.get("run", ""))
        summary = deploy["run"]
        self.assertIn("dns-zones", summary)
        self.assertIn("dns-services", summary)
        self.assertIn("automated-preview-only", summary)
        self.assertIn("templateLocation.quickCreateUrl", summary)
        self.assertNotIn("execute-change-set", summary)
        self.assertIn("SHIFTBOX_QUALIFICATION_BROWSER", deploy["env"])
        self.assertIn("SHIFTBOX_QUALIFICATION_STAFFED_SERVICE_ROLE_ARN", deploy["env"])
        staffed = next(s for s in qualify["steps"] if "--verify-staffed-dns" in s.get("run", ""))
        self.assertEqual("inputs.staffed", staffed["if"])
        self.assertIn("SHIFTBOX_QUALIFICATION_STAFFED_SERVICE_ROLE_ARN", staffed["env"])
        self.assertIn("SHIFTBOX_QUALIFICATION_BROWSER", staffed["env"])
        self.assertNotIn("execute-change-set", staffed["run"])

    def test_permanent_fixtures_are_outside_workflow_authority(self):
        """Admission and teardown never consume credentials for operator-owned fixtures."""
        workflow = str(self.workflow)
        for setting in ("TAILNET_READY", "SHIFTBOX_QUALIFICATION_TAILNET_", "tailnet.py",
                        "GitHubAppSecretArn", "get-secret-value"):
            self.assertNotIn(setting, workflow)
        self.assertIn("SHIFTBOX_QUALIFICATION_SUBNETS", workflow)
        self.assertNotIn("AccessMode=", workflow)

    def test_no_privileged_default_tokens(self):
        """The qualification workflow has no repository or package publishing authority."""
        self.assertEqual({"contents": "read"}, self.workflow["permissions"])
        for job in self.workflow["jobs"].values():
            for permission, access in job["permissions"].items():
                self.assertTrue(access == "read" or (permission, access) == ("id-token", "write"))


if __name__ == "__main__":
    unittest.main()
