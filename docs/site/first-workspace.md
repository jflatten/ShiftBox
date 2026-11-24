---
layout: default
title: Launch your first workspace
permalink: /first-workspace/
---

# Launch your first workspace

Open a GitHub repository in ShiftBox and start VS Code in your browser. If your administrator has already created a project and configured its GitHub App, start by [connecting your GitHub account](#connect-your-github-account).

## Before you begin

* Connect to your private ShiftBox installation and sign in.
* Choose a GitHub repository you can access that already includes a Dev Container configuration for x86-64. The configuration usually lives at `.devcontainer/devcontainer.json` and tells ShiftBox which tools to install.
* To create a project, sign in with an account that has permission to create one. To set up the GitHub App, you also need permission to register and install it for the account that owns the repository.

## Choose a workspace size

Choose a **Small**, **Medium**, or **Large** resource profile when you create a project. A resource profile determines the amount of computing power, memory, and disk space a workspace receives. Start with **Small** for editing and light builds. An administrator can review the available sizes under **Administration → Infrastructure → Resources**.

## Configure the GitHub App

A ShiftBox administrator completes this step once per installation. You need permission to register and install the app on the GitHub account that owns the repository.

1. Open **Administration → Settings → GitHub App**. Enter the GitHub organization that owns the repositories, or leave the field blank for a personal account.
2. Select **Set up GitHub App** and confirm the prefilled registration on GitHub. During installation, grant the app access to the intended repositories.
3. Keep your private connection open so your browser can return to ShiftBox. If an organization administrator approves the installation later, return to ShiftBox and select **Check installation**.

## Connect your GitHub account

1. In ShiftBox, open **Account** and select **Connect GitHub**.
2. Select **Continue to GitHub** and authorize the app with a GitHub account that can access the project's repository.
3. Keep your private connection open during the redirect. When you return to ShiftBox, check that **Account** shows GitHub as connected.

## Create a project and open the IDE

1. Go to **Projects → New project**. Enter a name and the GitHub **Repository URL**. Use **Repository ref** to choose a specific branch or tag, or leave it blank to use the repository’s default branch. Enter the repository-relative file path in **Dev Container configuration**.
2. Choose a **Resource profile** and select **Create project**. If an administrator has already created the project for you, open **Projects** and find it there.
3. Select **Launch workspace** on the project's row. Enter a workspace name, review the source and resources, and confirm with **Launch workspace**.
4. Open the workspace under **Workspaces** and follow its build progress. When **Open IDE** becomes available, open it. You should see VS Code in your browser with the repository files and a terminal running in the project's devcontainer.

Push changes you want to keep before deleting a workspace. Restarting or rebuilding preserves workspace data on its disk. Deleting the workspace removes that disk. [Troubleshoot workspace launch]({{ '/troubleshooting/' | relative_url }})
