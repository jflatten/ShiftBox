---
layout: default
title: Launch your first workspace
permalink: /first-workspace/
---

# Launch your first workspace

Open a GitHub repository in ShiftBox and start VS Code in your browser. These steps use the published RC3 release. If your administrator has already created a project and configured its GitHub App, start by [connecting your GitHub account](#connect-your-github-account).

## Before you begin

* Connect to your private ShiftBox installation and sign in.
* Choose a GitHub repository you can access with an amd64-compatible Dev Container configuration, such as `.devcontainer/devcontainer.json`. ShiftBox reads the file from the repository and ref you select.
* To configure the workspace image or create a project, sign in as a ShiftBox administrator or use an account with the relevant permissions. To set up the GitHub App, you also need permission to register and install it for the account that owns the repository.

## Validate the AWS workspace image

1. Open **Administration → Infrastructure → Platform**. Under **Official AWS release**, confirm the release and region match your installation.
2. Extract `shiftbox-aws-support.tar.gz` from the installed release. The guided installer saved a verified copy in the `release/` folder of its work directory. For an Ohio install, select `catalog/us-east-2.json` in **Official AMI catalog manifest**. Use the corresponding JSON file for any other region.
3. Select **Validate official AMI**. ShiftBox launches a temporary EC2 instance to verify that the image can enroll. Wait for **Validated** and confirm the validation operation completed and cleaned up the temporary resources.
4. Go to **Administration → Infrastructure → Resources** and select **New resource profile**. Give the profile a name, select the validated release under **AMI image**, and choose an instance type offered in your workspace Availability Zone. For example, if offered, `t3.medium` has 2 vCPUs and 4 GiB of memory. Set **EBS disk (GiB)** to at least 32 GiB for RC3 and save the profile.

## Configure the GitHub App

On RC3, the controller reads GitHub App credentials from the stack's `GitHubAppSecretArn` parameter. Complete these steps once per installation.

1. Register a GitHub App under the account that owns the repository. Use the stack's `ManagementOrigin` as the homepage URL. For the **Callback URL**, append `/api/v1/git/authorization/callback` to that origin, including its HTTPS port. The user's browser follows this callback over the private connection. [Registering a GitHub App](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app)
2. Disable **Webhook active**. Grant repository **Contents** read access for checkout. For teams that need to push code or work with pull requests and workflows, ShiftBox supports read and write access to **Contents** and **Pull requests**, write access to **Workflows**, and read access to **Metadata**, **Checks** and **Commit statuses**.
3. Install the app on the account that owns the project and grant access to the selected repository. On GitHub, find the numeric installation ID in the URL for that installation's settings. This ID identifies the installation of the app on the account.
4. Generate a client secret in the app's settings. In Secrets Manager in the stack's account and region, store a JSON object with `clientId` and `clientSecret` as strings and `installationId` as a number. Keep the secret value in Secrets Manager.
5. Create a CloudFormation update change set on the same stack and versioned template. Set `GitHubAppSecretArn` to the new secret's ARN and keep `Phase=active` and the other parameters unchanged. Review that the change preserves the installation volume and networking before executing it. Wait for the controller service to come back online and sign in again.

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
