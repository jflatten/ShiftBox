# Private ShiftBox on AWS

Terraform deploys a private VPC, an ECS-on-EC2 host, an independent encrypted data volume, ECR, separate ECS services for PostgreSQL and ShiftBox, private DNS, and a protected setup task. The deployer provides private network access or enables the optional Tailscale router.

## 1. Provision the foundation and publish the controller

Install Terraform, AWS CLI v2, the Session Manager plugin, jq and Docker. From this directory initialize Terraform with the checked-in lock. Supply `aws_region`, `availability_zone`, `vpc_cidr` and `operator_ipv4_cidrs` in a local `terraform.tfvars` file. Choose a VPC range that fits your private routing. Terraform outputs the private route table ID for deployer-managed routing. By default the two private subnets share one outbound NAT gateway. Set `create_nat_gateway = false` if the deployer supplies outbound connectivity.

```sh
terraform init -lockfile=readonly
terraform plan -out=foundation.tfplan
terraform apply foundation.tfplan
terraform output -raw controller_repository_url
```

Build the Linux amd64 [controller image](../../container/README.md), push it to the ECR repository with the release identity, and record its SHA-256 manifest digest. Keep the digest in `controller_image_digest` for the service deployment.

## 2. Start the private PostgreSQL service

Add these inputs to the local variables file.

| Input | Value |
| --- | --- |
| `ecs_host_ami_id` | Pinned ECS-optimized Amazon Linux 2023 amd64 AMI in the deployment region. |
| `initialize_data_volume` | `true` while initializing a new blank volume. Use `false` for host replacement. |
| `dns_suffix` | Private suffix resolvable within the VPC and through the deployer's private access. |
| `controller_image_digest` | Published image digest from the first step. |
| `postgres_image` | Digest-pinned PostgreSQL 17 Debian image reference. |
| `postgres_credentials_arn` | ARN of an existing Secrets Manager JSON secret with `admin_password`, `app_password`, and `dex_password`. |
| `postgres_tls_arn` | ARN of an existing Secrets Manager JSON secret with PEM `certificate` and `private_key` for `postgres.<dns_suffix>`. |
| `postgres_ca_pem` | Public PEM trust for the database certificate, supplied as a Terraform string. |
| `controller_enabled` | `false` during protected setup. |

Use Secrets Manager's AWS-managed key for the two secrets. Terraform receives the secret ARNs. ECS injects the values into the tasks that use them. PostgreSQL initializes separate `shiftbox_app` and `shiftbox_dex` roles and schemas on the blank EBS data directory, and its security group allows only the controller and setup tasks. The database certificate must identify `postgres.<dns_suffix>`.

Review and apply a new saved plan. Confirm the PostgreSQL ECS service is running. Attach the policy returned by `terraform output -raw setup_operator_policy_arn` to the authorized deployer identity, which also needs access to read the Terraform outputs.

## 3. Initialize and start ShiftBox

Run `./setup.sh` from a terminal with the authorized deployer's AWS session. It starts the setup task, prints the loopback URL and certificate SHA-256 fingerprint, and forwards the wizard through Session Manager. Verify the fingerprint before trusting the local setup certificate.
Set `SHIFTBOX_TERRAFORM_STATE` to the absolute state file path if your deployment uses a nondefault local state file.

In the wizard, create the administrator, record the one-time password, sign in, and change the password. Validate the preconfigured database and upload a certificate and key trusted by the controller for `shiftbox.<dns_suffix>`, `*.ide.<dns_suffix>`, `*.preview.<dns_suffix>` and `*.repair.<dns_suffix>`. Set workspace capacity and activate the installation. Close the tunnel to stop the setup task.

Set `controller_enabled = true`, review a new plan and apply it. Connect using your private network and sign in at `terraform output -raw management_origin`.

After proving stopped-instance enrollment confidentiality and recovery for a specific AMI, set `verified_enrollment_ami_id` to that AMI and apply the Terraform plan. An authenticated administrator can then submit the Packer manifest as `{"manifest": <Packer manifest object>, "makeDefault": true}` to `POST /api/v1/admin/platform-artifacts`. Use the normal session, CSRF and idempotency headers. The catalog enables the image only after the disposable guest authenticates and its owned resources are removed.

For new single-boot images, open **Administration → Infrastructure → Platform → AMI families**. Publish the Packer manifest under a family name and wait for validation to complete. Choose **latest** for that family when creating or editing a resource profile. The next workspace launch resolves the newest validated compatible member using its AWS creation time. Subsequent patches use the same family name. Existing workspaces keep their captured image and existing pinned profiles keep their selection.

Use **Retire** to remove an unused family image from future launches before deregistering it in AWS. ShiftBox retains the catalog identity and refuses retirement while a workspace still uses the image.

## Optional GitHub App

Register a GitHub App with the callback URL `<management_origin>/api/v1/git/authorization/callback` and install it on the account that owns the repositories. Store a JSON object with `clientId`, `clientSecret`, and numeric `installationId` in Secrets Manager before setting `github_app_credentials_arn`. The ECS controller execution role reads the named secret at task startup. Users connect their GitHub accounts from ShiftBox while connected to the private network.

## Optional Tailscale access

Set `tailscale_router_enabled = true`, review the Terraform plan, and apply it. Open a Systems Manager session to the instance returned by `terraform output -raw tailscale_router_instance_id`. On that instance, run the following command with the values from the deployment.

```sh
sudo tailscale up --hostname=<deployment-name>-tailscale --accept-dns=false --advertise-routes=<tailscale_advertised_route>
```

Open the authorization URL in your existing tailnet, then approve the advertised route in the Tailscale Machines page. In Tailscale DNS, add the router's Tailscale IPv4 address as a custom nameserver restricted to the Terraform `dns_suffix`. The router resolves that suffix through Route 53 and forwards connections to the ShiftBox HTTPS port. Grant the intended tailnet users access to the router on TCP and UDP port 53 and to the service subnet on TCP port 8443.

On a connected client using Tailscale DNS, open the `management_origin` output. If ShiftBox uses a private certificate authority, configure the client to trust that CA.

## Replace tasks or the host

The encrypted EBS volume is independent of host lifetime. For a planned host replacement, verify the management backup, stop both ECS services, and keep the volume in the same Availability Zone with initialization disabled. The replacement host mounts the original volume before ECS registers its capacity.

## Local validation

```sh
terraform init -backend=false -lockfile=readonly
terraform fmt -check -recursive
terraform validate
terraform test
sh host/mount-data.test.sh
```

## Optional dedicated registry cache disk

Use a separate approval for this storage change on a running deployment. Set
`registry_cache_volume_size_gib = 20` for the demo and leave
`registry_cache_mount_ready = false` for the initial review. The storage-only
plan should add an encrypted gp3 volume and its `/dev/sdg` attachment. Reject any
unexpected host, existing data disk, or service change. Existing Terraform
deployments can omit both settings.

After approval and attachment, obtain the exact ID from
`terraform output -raw registry_cache_volume_id`. Verify that ID is the newly
attached cache volume, distinct from the root and installation/PostgreSQL disks.

For a sealed, EBS-backed ECS host, create and verify a management backup before
maintenance. Quiesce the controller with `controller_enabled = false`, then
quiesce PostgreSQL with `registry_cache_postgres_enabled = false`. Review a plan
with `initialize_data_volume = false` and `registry_cache_bootstrap = true`.
The plan must update the existing instance's user data in place. Terraform
stops and starts the instance. Expect both services to be offline during this
maintenance. Abort a plan that would replace the instance or change any disk
attachment. After startup, require a fresh `SHIFTBOX_REGISTRY_CACHE_READY` line in
the EC2 console with the expected volume, filesystem and capacity. Verify the
original instance and disk IDs and restore PostgreSQL before the controller.

An authorized host maintenance channel can also run
`host/mount-registry-cache.sh` with the verified ID,
`/var/lib/shiftbox-registry-cache`, and `true` only for the initial blank volume
format. This script selects the Nitro device by exact serial, refuses foreign
mounts/filesystems, and prepares an owned cache directory and a volume marker.
Install the script to `/usr/local/libexec/shiftbox-mount-registry-cache` and the
provided systemd template to `/etc/systemd/system/`. Enable
`shiftbox-registry-cache@<volume-id>.service` for future boots. Its boot mount
never formats a volume. Verify `findmnt`, the marker and filesystem capacity
before generating the controller mount plan.

Set `registry_cache_mount_ready = true` in the second plan only after verifying
the mount. The bind is exclusive to the controller and its task revision
changes, with the usual brief demo controller interruption. It supplies the
expected volume ID so the cache can refuse retention if the mount is missing.
Use `/var/lib/shiftbox-registry-cache/cache` as the cache path in Administration
→ Infrastructure → Cache. Compare the reviewed plan before applying
and verify PostgreSQL remains healthy. Keep the volume attached and mounted
across controller rollouts. To retire it, disable cache retention, remove the
task bind, then unmount before any separately reviewed detach. The volume is
protected against accidental Terraform destruction.
