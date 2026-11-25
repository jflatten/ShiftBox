---
layout: default
title: Upgrade and recovery
permalink: /upgrade-recovery/
---

# Upgrade or recover a ShiftBox installation

Plan a maintenance window and keep the existing CloudFormation stack, Availability Zone, installation identity and retained storage. A single-controller deployment can interrupt sign-in and IDE access while the controller is stopped or replaced. Keep the installed controller build and its verified backup available until the new controller is healthy.

## Verify a management backup

1. Open **Administration → Backup & Recovery**. In **Destination**, enter a new absolute archive path on the controller. Enter a **Passphrase** of at least 12 characters and select **Create backup**. Wait for the archive to be verified. The archive covers the ShiftBox and Dex schemas, installation configuration, trust material and installation key. Keep the archive, its verified operation and checksum, and the separately stored passphrase in protected operator custody. Back up workspace VM disks and project data separately if required.
2. Use the CLI alternative only in the authorized private controller environment with the active configuration and backup socket. Use a new destination and enter the passphrase at the prompt.

   ```sh
   shiftbox backup --config "$active_config" --output "$backup_archive"
   ```

3. Match the recovery information to the installed controller build and PostgreSQL schema. Verify the backup before stopping normal service for irreversible application or Dex schema changes. Preserve the current template, controller digest and secret references separately.

## Review the upgrade change set

1. Review the changes between the installed revision and the version you plan to install. Verify the rendered template checksum, exact regional AMI, controller image digest, and guest compatibility. Check PostgreSQL ownership and TLS, compatible backup/restore tools, writable retained storage, encryption key availability, and expiry of the browser certificate and guest CA. Before irreversible schema changes, satisfy the all-retained-workspaces-current gate. Resolve any stopped, unreachable or unverified workspace reported by that gate.
2. Use the same stack and the verified target template to create an update change set. Keep termination protection and the stack policy. Confirm that the retained EBS volume, its Availability Zone, account, installation identity and PostgreSQL secret references stay correct. Stop for replacement of the volume, secret or host, unexpected networking/IAM changes or a stale change set. Review those changes separately before attempting them.
3. Read the changes and validation findings before executing the reviewed change set. Compare with the live stack and create a fresh change set if inputs or stack state changed.

   ```sh
   aws cloudformation describe-change-set --region "$region" --change-set-name "$change_set_arn"
   aws cloudformation describe-events --region "$region" --change-set-name "$change_set_arn"
   ```

4. If the upgrade includes a schema migration, stop the normal controller through a reviewed change set before migration. Keep the database and retained volume in place. Use the migration procedure for the target revision after verifying the backup and satisfying the retained-workspace gate. Confirm that the procedure covers your starting schema before stopping the installed controller.
5. After migration or a reviewed controller-only replacement, check that CloudFormation and both ECS services stabilize. Sign in again at **ManagementOrigin** from an allowed network, verify the installation identity and retained workspace inventory, and open an authorized workspace IDE. Check the running controller digest and volume identity against your record.

## Recover from a failed migration

1. Keep the normal controller stopped. Preserve the existing stack, volume, failed database and evidence. Retrieve the matching controller image and its verified management archive from before the migration. Restore to an empty replacement PostgreSQL target with separately scoped ShiftBox and Dex schema owners, TLS verification and compatible restore utilities. Workspace disks and external services have their own state and recovery procedures.
2. Run the matching ShiftBox binary from an authorized private recovery environment with access to this installation's retained volume and provider binding. The example uses absolute paths. Use a private recovery configuration tied to the original installation directory and key, with the replacement application connection in `postgresURI`. Put the replacement Dex connection in `dexUri` and the trusted CA path in `caFile` in a separate private file. Keep credentials and private keys out of command arguments and shell output. Use the exact build identifier recorded in the archive for the `--release` argument.

   ```sh
   shiftbox restore validate --config "$recovery_config" --archive "$backup_archive" \
     --installation-id "$installation_id" --release "$archive_build_id" \
     --destination "$replacement_dir" --postgres-config "$identity_config"
   ```

   Resolve invalid archive, key, build identifier, and target errors before continuing. If a restore attempt partially loaded either schema, prepare another empty owned replacement and validate it again.
3. Record the returned validation ID. Apply the reviewed restore with the normal controller stopped. Start the recovery listener using the returned configuration on the approved private route.

   ```sh
   shiftbox restore activate --config "$recovery_config" --archive "$backup_archive" \
     --validation-id "$validation_id" --destination "$replacement_dir" \
     --postgres-config "$identity_config"
   shiftbox serve --config "$replacement_dir/configuration.json"
   ```

4. Sign in as the administrator on the restricted listener. Review identities, reconcile provider inventory against retained EC2 instances and disks, then review authorization and retention. Old sessions and access or enrollment grants stay invalid. Activate explicitly after review. Use a fresh sign-in and verify normal management and authorized IDE access before completing the stack and database cutover. Keep the former runtime fenced.

If you need to diagnose a blocked rollout, collect redacted CloudFormation operation and ECS status details with the approved AWS identity. Keep private endpoints, account and workspace identifiers, archives, connection strings and keys in your operator evidence store. [Troubleshoot an AWS installation]({{ '/troubleshooting/' | relative_url }})

## Rehearse the recovery path

Rehearse a restore on a disposable installation with the same controller build as the backup. Exercise the upgrade on a trial stack with the version you plan to install, including the migration gates and fresh administrator review. Record the exact controller digest and archive used. [Install ShiftBox]({{ '/install/' | relative_url }})
