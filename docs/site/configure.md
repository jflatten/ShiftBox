---
layout: default
title: Configure ShiftBox for your team
permalink: /configure/
---

# Configure ShiftBox for your team

Sign in as a ShiftBox administrator after activation. To try one repository first, connect GitHub and launch a workspace. The settings below help you decide what to change before inviting the rest of your team. [Launch your first workspace]({{ '/first-workspace/' | relative_url }})

## Workspace sizes and capacity

Open **Administration → Infrastructure → Platform**. The **Provider** section shows what the deployment can run. A *resource profile* sets the size of one machine. The *capacity budget* limits what all workspaces can reserve together.

| Choice | What it controls |
| --- | --- |
| **Resource profiles** | Choose the EC2 instance type for CPU and memory and the EBS disk size for files and builds. More memory helps large builds. A larger disk helps repositories with large images or dependencies. Disabling a profile prevents new launches that use it. |
| **AMI image** | An Amazon Machine Image (AMI) contains the workspace operating system and guest agent. Select a validated release image to pin a profile to it. Select an AMI family if new workspaces should use its latest compatible validated image. |
| **Capacity budget** | Set total vCPU, memory and disk reservations. For example, an 8-vCPU limit admits four 2-vCPU workspaces if enough memory and disk also remain. A confirmed stop releases CPU and memory. Deletion releases disk. Leave a limit blank for unlimited. AWS quotas and charges still apply. |
| **Inactivity shutdown** | The default idle period is 30 minutes. Set the maximum your organization permits, the list of choices developers may select, and the default from that list. **Unlimited** keeps an idle machine running. Fresh installs offer 30 minutes, 1, 2, 4, 8 or 12 hours, and Unlimited. Shorter timeouts reduce idle compute charges. Each workspace can choose an allowed value under **Settings**. |
| **Launch log retention** | Keep completed launch diagnostics for 1–3,650 days. The default is 30 days. Choose longer if you need to investigate older launches. ShiftBox checks for expired diagnostics every four hours. |

### When the resource profile list is empty

1. Under **Platform → Official AWS release**, choose **Official AMI catalog manifest** and select the file for your Region in the matching release archive, for example `catalog/us-east-2.json`.
2. Select **Validate official AMI**. Wait for the check to finish and clean up its temporary EC2 instance. Look for **Validated**.
3. Under **Resource profiles**, select **New resource profile**. Give it a name developers will recognize, select the validated **AMI image**, choose an available **EC2 instance type**, and enter an **EBS disk (GiB)** size within the displayed range. Save the profile. It should now appear in the project’s **Resource profile** menu.

## Image caching

Open **Administration → Infrastructure → Cache**. Caching helps when people repeatedly download the same public images or launch the same project. Both switches default to on in a fresh installation using the revised controller. Existing installations retain their saved choices.

| Setting | What happens when you use it |
| --- | --- |
| **Retain cached image content** | Saves image layers fetched through the controller from public registries. Later requests for those layers can use the saved copy. |
| **Built devcontainer images** | Saves a finished image for eligible future launches of the same project. The new workspace still checks out its own files and runs its setup. Turning this off stops reuse and removes stored built images in the background. |
| **Cache size (MiB)** | A fresh installation starts with a 14 GiB budget (14,336 MiB). Set how much content the cache may keep. The two kinds of images share this budget and the underlying disk. Increasing the disk size in CloudFormation and increasing this budget are separate decisions. |
| **High watermark (%)** and **Low watermark (%)** | The defaults are 80% high and 70% low. When disk usage reaches the high threshold, ShiftBox removes eligible cached content to bring usage down toward the low threshold. Leave headroom for active transfers. |
| **Cache storage location** | Keep the installation’s cache path to use its provisioned disk. Changing the path lets active pulls finish and starts a new cache at the new location. Old files continue to occupy the previous disk until reclaimed. |

Select **Save cache settings** for the public cache controls or **Save built image settings** for finished images. Check the effective budget and status, then refresh. Under **Cached images**, **Public registry** identifies downloaded layers and **Devcontainer build** identifies a completed project image. A successful workspace can become ready before its image upload finishes. [Troubleshoot a missing built image]({{ '/troubleshooting/' | relative_url }}#browser-and-workspace-problems)

## Sign-in and user access

- **Regular sessions.** In **Administration → Settings → Sessions**, set **Session timeout** in whole minutes, hours or days, up to 400 days, then select **Save session timeout**. A fresh installation defaults to eight hours for normal sign-ins. First-login setup uses 30 minutes. Changes apply at the next sign-in. This controls how long the browser session lasts. **Inactivity shutdown** controls the running machine.
- **Accounts.** Under **Administration → Users**, select **Add local account** and enter the person’s email. Choose **Member** for a developer or **Administrator** for someone who manages the installation. Save the one-time temporary password and deliver it privately. The user changes it at first sign-in.
- **GitHub access.** Under **Administration → Integrations**, register the GitHub App and select its repositories. Each developer then connects their own GitHub account. Use **Manage repository access on GitHub** to change the selection. **Revoke access** removes the installation and interrupts repository access for the team. [Connect GitHub]({{ '/first-workspace/' | relative_url }}#configure-the-github-app)
- **Project access.** In **Projects**, open the project’s **Actions → Share launch access**. Sharing lets the recipient launch their own workspace and use the project’s secrets. Review those secrets before granting access.

## Secrets and browser certificates

A *secret* is a credential such as a package registry token. Use **Administration → Settings → Secrets** for values everyone is authorized to use, **Projects → Actions → Project secrets** for one project, and **Account → Personal secrets** for individual credentials. If the same name exists in several places, the personal value wins, then the project value, then the organization value. The UI hides saved values. Code running in a workspace can access the secrets its owner is authorized to use there.

Under **Administration → Settings → Certificates**, check the active certificate’s names, issuer and expiry. For Let’s Encrypt, check the last renewal result. For operator-managed certificates, use **Replace certificate** with the PEM certificate chain and its matching private key. Cover the management name and the IDE, preview and repair wildcards. Distribute the issuing CA to clients if needed. [Browser trust and network access]({{ '/private-access/' | relative_url }})

## Maintenance and backups

Use **Administration → Infrastructure → Maintenance** to review affected workspaces and schedule a guest agent update. Supply the release ID and a deadline in your browser’s local time. The guest agent runs inside each workspace, so an existing workspace may need a compatible signed guest update to report detailed timings.

To announce a maintenance window, use **Administration → Notices**. Choose a severity, title and message. Set a local start and expiry time to limit when users see it. **Administration → Audit** lets you review recorded administrative actions.

Before upgrading the controller or making a destructive change, open **Administration → Backup & Recovery** and verify a management backup. It covers installation settings, identities and trust material. Save project work to Git and protect workspace disks separately. [Upgrade and recovery procedure]({{ '/upgrade-recovery/' | relative_url }})
