---
layout: default
title: ShiftBox on AWS
permalink: /
---

# Run ShiftBox in your AWS account

ShiftBox gives each developer a virtual machine called a *workspace*, with VS Code in the browser. A *project* tells ShiftBox which GitHub repository to use, how to set up its tools and how large the machine should be. The *controller* is the web application where you manage these projects, workspaces and users.

## From an existing AWS network to an open editor

1. **Choose how people will reach ShiftBox.** Use a private connection if your team already connects to your Amazon Virtual Private Cloud (VPC). Use public HTTPS if they need to connect over the internet from approved addresses. Public access requires a domain you control. [Compare deployment options]({{ '/install/' | relative_url }}#get-the-installation-files)
2. **Install the controller.** With a current launch form, create one CloudFormation stack, then complete first-login setup in the browser. Start by checking which installation files your release includes. [Install ShiftBox on AWS]({{ '/install/' | relative_url }})
3. **Open the first workspace.** Connect GitHub, choose a project size, launch a repository and select **Open IDE**. At that point you have tested the path your developers will use. [Launch your first workspace]({{ '/first-workspace/' | relative_url }})

## Before inviting your team

Decide who can create and launch projects, how long people stay signed in, and how much compute and disk space the team can use. [Configure ShiftBox for your team]({{ '/configure/' | relative_url }})

If setup stops, start with the symptom on screen. Before an upgrade or teardown, verify a backup and save the workspace files you want to keep. [Troubleshoot an installation]({{ '/troubleshooting/' | relative_url }}) · [Back up and recover ShiftBox]({{ '/upgrade-recovery/' | relative_url }})
