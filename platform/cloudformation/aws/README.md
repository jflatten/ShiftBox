# Install ShiftBox on AWS

Use the region link from the [release page](https://github.com/jflatten/ShiftBox/releases) for the version you intend to install. Each link opens the CloudFormation review page for the same template bytes available for download in the release. Compare the SHA-256 in the distribution manifest before using a downloaded template or support archive.

ShiftBox runs a private ECS controller and PostgreSQL on an EC2 host with retained encrypted installation storage. Workspaces run on separate private EC2 instances. Review costs for the host, volumes, private DNS, outbound data and each workspace. Use an AWS identity authorized to create the stack and acknowledge its IAM resources. The protected browser setup requires AWS CLI v2 and the local Session Manager plugin.

## Start with the foundation

1. Confirm the intended AWS account and one of the release's four regions. Inspect the exact versioned template and select a unique deployment name. Start with `Phase=foundation`.
2. Select the existing VPC and private service and workspace subnets. The service subnet hosts the ECS host, browser controller, PostgreSQL and setup. Set the Availability Zone to the service subnet's zone so the retained volume can attach to the host. Both subnets must belong to the selected VPC. Choose a unique private DNS suffix, such as `sb-<random>.internal`. Leave `OperatorCidr` blank for closed browser ingress, or use the approved IPv4 source CIDR your service will see. Provide existing outbound connectivity before deployment.
3. Keep `RoleBoundaryMode=managed` for quick-create and customer installations. Role and instance profile names include the deployment name and region because IAM names are account-wide. Review the named roles and their per-role permissions boundaries, then acknowledge named IAM resources in the Console or use `CAPABILITY_NAMED_IAM` when creating a change set through the AWS CLI. Enable termination protection and use `stack-policy.json` from the verified support archive for stack updates. The stack policy protects the installation volume, its attachment, host and managed secrets. Preview the foundation change set or review the initial quick-create page before creating resources. [AWS IAM capability acknowledgement](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_CreateChangeSet.html)
4. Keep the stack ID, `VpcId`, subnet IDs and installation volume ID in your operator records. Foundation creates independent database password secrets. The stack holds DNS creation, database startup and controller startup for their later phases.

The guided CLI follows the same phases using explicit CloudFormation change-set approval. Use the command on the selected release page for stable or a pinned version. Continue an interrupted guided installation with `--resume --work-dir <same-directory>`. Use the documented upgrade procedure if changing the pinned release.

## Size the OCI image cache

Set **OCI image cache size (GiB)** when creating the stack. The default is 20 GiB
for up to five active users. Start with 50 GiB for 6 to 20 users or 100 GiB for
21 to 50. Increase the starting size when projects use many large, distinct
images. Shared layers reduce disk usage. Review the storage budget under
**Administration → Infrastructure → Cache** when choosing a larger disk.

The controller uses a separate encrypted gp3 volume that is deleted with the
stack. To increase the size on a stack that already has that volume, review a
size-only change set and wait for the EBS modification to complete. Restart the
ECS host during planned maintenance to grow the filesystem. EBS volumes cannot
be reduced in place. [Amazon EBS volume modification](https://docs.aws.amazon.com/ebs/latest/userguide/ebs-modify-volume.html)

An installation created before this option needs a separate host bootstrap
change before it can use a dedicated cache disk.

## Stage private DNS

1. Create an UPDATE change set on the same stack and template with `Phase=dns-zones` and the other parameters unchanged. Review the addition of `PrivateZone` and `DiscoveryNamespace` and the service-side validations before executing. When it completes, record the `PrivateZoneId`, `DiscoveryNamespaceId` and `DiscoveryHostedZoneId` outputs. In Route 53, verify that the exact two hosted zones are private and associated with the stack's `VpcId`. In Cloud Map, confirm the namespace is `services.<your-private-suffix>` and reports the matching discovery hosted zone.
2. Review and execute a second UPDATE with `Phase=dns-services`. This adds `PostgresDiscovery` and `ControllerDiscovery`. Check that `PostgresDiscoveryServiceId` and `ControllerDiscoveryServiceId` identify the `postgres` and `shiftbox` services in the verified namespace. After both DNS changes, confirm the same VPC and retained volume IDs. The application aliases are created in the database phase.

## Prepare database TLS and the setup task

1. Use stack Outputs to find `PostgresHostname` and `PostgresTlsSecretArn`. Using your organization CA or a new authority scoped to this installation's database, issue a server certificate with DNS SAN matching `PostgresHostname`. Store its PEM certificate and private key as `certificate` and `private_key` in the exact `PostgresTlsSecretArn` through Secrets Manager. Keep the issuer private key in protected operator storage.
2. Preview an UPDATE change set on the same stack and template. Set `Phase=database` and supply the **public CA certificate only** in `PostgresCaPem`. You can replace PEM line breaks with `|` for a single-line console input. Inspect the change set and all deployment validations. Reject unexpected removal or replacement of persistent resources, and verify the same volume and AZ.
3. Execute the approved change set and wait for PostgreSQL to be ready. Have an authorized administrator grant the intended bootstrap operator the policy identified by `SetupOperatorPolicyArn`. The operator also needs read access to that CloudFormation stack and the local Session Manager client.

## Connect privately and complete the wizard

Use your existing Tailscale subnet router, VPN, Direct Connect or other private connection. Arrange forward and return routes to the selected service subnet, verify TCP 8443 from the approved client source, and forward DNS for the deployment suffix to a reachable VPC-side resolver. ShiftBox creates its own security groups and installation DNS records within the selected VPC. Your deployer owns subnet routing, egress, network ACLs, DNS forwarding and router lifecycle. Confirm that the management and wildcard workspace names use private addresses. PostgreSQL TCP 5432 is restricted to the controller and setup groups. Guest ports TCP 19443–19447 are scoped to the workspace security group.

For private access without a registered domain, issue a separate browser leaf from a new installation-specific private CA. Its DNS SANs are `shiftbox.<suffix>`, `*.ide.<suffix>`, `*.preview.<suffix>` and `*.repair.<suffix>`. Use a normal OS/browser trust-store import for the initial one-time trust step. Compare the CA fingerprint using `openssl x509 -sha256 -fingerprint -noout -in <ca.pem>` before trusting it. Retain its issuer key securely and record renewal and trust removal procedures. An operator with a trusted certificate covering these same names can supply it directly. Keep the separate database and guest trust contexts.

With OpenSSL that supports `req -addext`, create the browser leaf and a distinct database certificate from a protected local directory. Set `dns_suffix` to the exact value you entered for the stack.

```sh
umask 077
issue_tls() (
  set -eu
  mkdir -m 700 "$1"
  cd "$1"
  openssl req -new -x509 -newkey rsa:3072 -nodes -sha256 -days 1825 \
    -subj "/CN=ShiftBox $1 CA" -addext 'basicConstraints=critical,CA:TRUE,pathlen:0' \
    -addext 'keyUsage=critical,keyCertSign,cRLSign' -keyout ca.key -out ca.pem
  openssl req -new -newkey rsa:2048 -nodes -subj "/CN=$2" -keyout leaf.key -out leaf.csr
  printf 'basicConstraints = critical,CA:FALSE\nextendedKeyUsage = serverAuth\nsubjectAltName = %s\n' "$3" > leaf.ext
  openssl x509 -req -sha256 -days 397 -in leaf.csr -CA ca.pem -CAkey ca.key \
    -CAcreateserial -extfile leaf.ext -out leaf.pem
  openssl verify -CAfile ca.pem leaf.pem
)
issue_tls database-tls "postgres.$dns_suffix" "DNS:postgres.$dns_suffix"
issue_tls browser-tls "shiftbox.$dns_suffix" \
  "DNS:shiftbox.$dns_suffix,DNS:*.ide.$dns_suffix,DNS:*.preview.$dns_suffix,DNS:*.repair.$dns_suffix"
```

Use `database-tls/leaf.pem` and `database-tls/leaf.key` for the database secret, and `database-tls/ca.pem` for the public stack parameter. Import only `browser-tls/ca.pem` in the client trust store. Upload `browser-tls/leaf.pem` and `browser-tls/leaf.key` through the setup wizard.

Extract the verified support archive. Run its setup helper from a local terminal using the stack ID, region and expected account.

```sh
sh ./setup.sh "$stack_id" "$region" "$account_id"
```

Use the HTTPS setup link over the local SSM port forward and verify its displayed SHA-256 fingerprint. Create the initial administrator and change their temporary password. Select the verified PostgreSQL profile, upload the browser TLS certificate and leaf key, configure capacity and activate the installation. Close the local session after activation. The helper waits for durable activation and confirms the setup task stops.

## Start the controller and first workspace

Review an UPDATE change set for `Phase=active` on the same template and stack. Confirm the change preserves the EBS volume, secret references and private ingress. Start the controller only after the protected setup has completed and the setup task has stopped.

Open `ManagementOrigin` from stack Outputs on a permitted private client and sign in. Under **Administration → Infrastructure → Platform**, import and validate the exact official AMI using the selected region's catalog file in the support archive. Observe the disposable canary and cleanup. Create a named pinned resource profile and choose it in the project template. Connect the workspace owner's GitHub account to authorize their repository checkout, launch the workspace and open its IDE.

## Diagnose and recover

Check CloudFormation operation validation and failure events before retrying. Confirm your console or AWS CLI identity, stack region, exact template version, ECS service and task health, mounted installation volume, secret references, private DNS/routes and trust chain. An untrusted certificate or private DNS error calls for correcting access or trust. Keep the original stack, AWS resources and protected setup identity during an interrupted install. Follow the version-matched backup, migration and recovery guide before changing the release or deliberately removing retained resources.
