---
layout: default
title: ShiftBox on AWS
permalink: /
---

# Run ShiftBox in your AWS account

ShiftBox is an open-source development sandbox for you and your coding agent. Each workspace runs in its own EC2 virtual machine and uses the tools defined by your repository's Dev Container configuration. Open the workspace in VS Code in your browser to work with the same files, tools and runtime.

## Get started

1. [Install ShiftBox on AWS]({{ '/install/' | relative_url }}), using the CloudFormation template from `main` with your existing VPC and private subnets.
2. [Connect to ShiftBox privately]({{ '/private-access/' | relative_url }}), using Tailscale or a private connection to your AWS network.
3. [Launch a workspace from a GitHub repository]({{ '/first-workspace/' | relative_url }}) and open its browser IDE.

Already running ShiftBox? Find help with [AWS installation problems]({{ '/troubleshooting/' | relative_url }}) or [back up and upgrade your installation]({{ '/upgrade-recovery/' | relative_url }}).
