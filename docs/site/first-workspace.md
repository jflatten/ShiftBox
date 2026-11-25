---
layout: default
title: Launch your first workspace
permalink: /first-workspace/
---

# Launch your first workspace

Open a GitHub repository in ShiftBox and start VS Code in your browser. If your administrator has already created a project and configured its GitHub App, start by [connecting your GitHub account](#connect-your-github-account).

## Before you begin

* Open **ManagementOrigin** from an allowed network and sign in. **Projects** is the home page.
* Choose a GitHub repository you can access that already includes a Dev Container configuration compatible with your workspace image (x86-64 for the official release). The configuration usually lives at `.devcontainer/devcontainer.json` and tells ShiftBox which tools to install.
* To create a project, sign in with an account that has permission to create one. To set up the GitHub App, you also need permission to register and install it for the account that owns the repository.

## Choose a workspace size

Open **Administration → Infrastructure → Platform → Resource profiles** as an administrator. The profile sets the CPU, memory and disk for each workspace launched from a project.

If starter profiles are present, choose **Small** for editing and light builds, **Medium** for builds that need more memory, or **Large** for parallel builds and supporting services. Check the listed EC2 type and disk size before choosing. If the list is empty, validate the regional image and create a profile before creating the project. [Set up workspace sizes and capacity]({{ '/configure/' | relative_url }}#workspace-sizes-and-capacity)

## Configure the GitHub App

A ShiftBox administrator completes this step once per installation. You need permission to register and install the app on the GitHub account that owns the repository.

1. Open **Administration → Integrations** and find **GitHub Integration**. Enter the GitHub organization that owns the repositories, or leave the field blank for a personal account.
2. Select **Set up GitHub App** and confirm the prefilled registration on GitHub. During installation, grant the app access to the intended repositories.
3. Return to ShiftBox in the same browser. If an organization administrator approves the installation later, return to ShiftBox and select **Check installation**.

## Connect your GitHub account

1. In ShiftBox, open **Account** and select **Connect GitHub**.
2. Select **Continue to GitHub** and authorize the app with a GitHub account that can access the project's repository.
3. After the redirect, check that **Account** shows GitHub as connected.

## Create a project and open the IDE

1. Go to **Projects → New project**. Enter a name and the GitHub **Repository URL**. Use **Repository ref** to choose a specific branch or tag, or leave it blank to use the repository’s default branch. Keep **Dev Container configuration** at `.devcontainer/devcontainer.json` if that is where the file lives, or enter its path from the repository root.
2. Choose a **Resource profile**. For a first project, leave **Network policy ID** and **Secret names** blank. The workspace uses the default shared network. If your administrator supplied a compatible network policy, enter its ID here. If the project references credentials, enter their names separated by commas and store the values in the appropriate scope. [Manage secrets]({{ '/configure/' | relative_url }}#secrets-and-browser-certificates)
3. Select **Create project**. The project now appears under **Projects**.
4. Select **Launch workspace** on the project's row. Enter a workspace name, review the source and resources, and confirm with **Launch workspace**.
5. Open the workspace under **Workspaces** and follow its build progress. When **Open IDE** becomes available, open it. You should see VS Code in your browser with the repository files and a terminal running in the project's devcontainer.

## Check a launch or start

Open **Workspaces**, select the workspace and open **Performance**. A new launch or an accepted start clears the previous measurements. **Time to IDE ready** measures when you can open the editor. **Time to setup complete** includes the remaining setup work. **VM running to guest contact** measures the delay between EC2 reporting the machine running and its agent contacting ShiftBox. Details appear as they are reported. Older guest agents can leave some rows unavailable.

Use **Logs** to investigate a failed step. For a repeat test, stop the workspace, wait for **Stopped**, then select **Start workspace** and watch **Performance** for the new run.

Push changes you want to keep before deleting a workspace. Restarting or rebuilding preserves workspace data on its disk. Deleting the workspace removes that disk. [Troubleshoot workspace launch]({{ '/troubleshooting/' | relative_url }})
