---
layout: default
title: Launch your first workspace
permalink: /first-workspace/
---

# Launch your first workspace

Open a GitHub repository in ShiftBox and start VS Code in your browser. If your administrator has already created a project and configured its GitHub App, start by [connecting your GitHub account](#connect-your-github-account).

## Before you begin

* Connect to your private ShiftBox installation and sign in.
* Choose a GitHub repository you can access with an amd64-compatible Dev Container configuration, such as `.devcontainer/devcontainer.json`. ShiftBox reads the file from the repository and ref you select.
* To configure the workspace image or create a project, sign in as a ShiftBox administrator or use an account with the relevant permissions. To set up the GitHub App, you also need permission to register and install it for the account that owns the repository.

## Validate the AWS workspace image

If setup supplied **Small**, **Medium**, and **Large** resource profiles, choose one for your project. You can review the profiles under **Administration → Infrastructure → Resources**. For a staged installation without a profile, follow these steps.

1. Open **Administration → Infrastructure → Platform**. Select your deployment's regional JSON file under **Official AMI catalog manifest**.
2. Select **Validate official AMI**. ShiftBox launches a temporary EC2 instance to verify that the image can enroll. Wait for **Validated** and confirm the validation operation completed and cleaned up the temporary resources.
3. Go to **Administration → Infrastructure → Resources** and select **New resource profile**. Give the profile a name and select the validated **AMI image**. Choose an instance type offered in the workspace subnet's Availability Zone and an **EBS disk (GiB)** size at or above the displayed minimum. Save the profile.

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

1. Go to **Projects → New project**. Enter a name and the GitHub **Repository URL**. Set **Repository ref** if you want a specific branch or tag. Enter the repository-relative file path in **Dev Container configuration**.
2. Choose the **Resource profile** you saved and select **Create project**. If an administrator has already created the project for you, open **Projects** and find it there.
3. Select **Launch workspace** on the project's row. Enter a workspace name, review the source and resources, and confirm with **Launch workspace**.
4. Open the workspace under **Workspaces** and follow its build progress. When **Open IDE** becomes available, open it. You should see VS Code in your browser with the repository files and a terminal running in the project's devcontainer.

Push changes you want to keep before deleting a workspace. Restarting or rebuilding preserves workspace data on its disk. Deleting the workspace removes that disk. [Troubleshoot workspace launch]({{ '/troubleshooting/' | relative_url }})
