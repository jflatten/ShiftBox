"""Render the task boundaries from their existing inline IAM policy documents."""

import re
import textwrap


TASK_BOUNDARIES = {
    "POSTGRES_EXECUTION_BOUNDARY_DOCUMENT": "ReadPostgresCredentials",
    "SETUP_EXECUTION_BOUNDARY_DOCUMENT": "ReadSetupCredentials",
    "CONTROLLER_EXECUTION_BOUNDARY_DOCUMENT": "ReadControllerCredentials",
    "SETUP_TASK_BOUNDARY_DOCUMENT": "SessionManagerChannels",
    "CONTROLLER_TASK_BOUNDARY_DOCUMENT": "OwnedWorkspaces",
}


def render_task_boundaries(template):
    """Copy each entire policy as YAML, including CloudFormation intrinsics."""
    for token, name in TASK_BOUNDARIES.items():
        marker = f"@@{token}@@"
        definitions = re.findall(
            rf"^        - PolicyName: {re.escape(name)}\n"
            r"          PolicyDocument:\n((?: {12}[^\n]*\n|\n)+)",
            template,
            re.MULTILINE,
        )
        if template.splitlines().count(marker) != 1 or len(definitions) != 1:
            raise ValueError(f"Expected exactly one source and boundary marker for {name}")
        policy = textwrap.indent(textwrap.dedent(definitions[0]), " " * 8)
        template = template.replace(f"{marker}\n", policy)
    return template
