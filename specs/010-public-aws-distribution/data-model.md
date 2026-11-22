# Data Model: First Public AWS Distribution

This design uses versioned release data, CloudFormation stack state and existing catalog records. It introduces no release database.

## Public release

- `tag`: Required and unique. Allowed forms are `vMAJOR.MINOR.PATCH` and `vMAJOR.MINOR.PATCH-rc.N`; numeric components have no leading zeros and `N` is positive.
- `channel`: `rc` or `stable`, consistent with GitHub prerelease status and the tag.
- `sourceCommit`: The fully peeled, reviewed source commit.
- `payloadManifest`: Required immutable release asset after publication. The stable release has the same manifest bytes as the chosen RC.
- Stable tag annotation names exactly one same-core RC tag and the exact reviewed qualification commit.

Transitions: tag received → checks passed → draft assembled → complete public prerelease or stable release. Published payload is immutable. Reconciliation reports a conflict on identity drift and keeps the last eligible default stable.

## Distribution manifest

- `schemaVersion`: Required integer equal to `1`.
- `candidateTag` and `sourceCommit`: Required immutable RC identity. Runtime compatibility version equals `candidateTag`.
- `controller`: Required approved GHCR repository, `sha256:` plus 64 lowercase hexadecimal digits, and platform `linux/amd64`.
- `template`, `installer` and `supportAssets`: Required fixed release asset names and SHA-256 checksums of exact bytes. The template is a standalone CloudFormation document. The support archive contains catalog manifests, stack policy, the stack-output setup helper and versioned operator guides.
- `publisherAccountId`: Required approved 12-digit AWS account ID, distinct from the installation account.
- `postgresImage`: Required approved PostgreSQL 17 Debian-based reference pinned by digest.
- `regions`: Exactly one entry each for `us-east-1`, `us-east-2`, `us-west-1`, and `us-west-2`. Every entry includes a vetted regional ECS host AMI pin, the official workspace image descriptor and the regional template location.
- `workspaceImage`: Required region-local AMI ID, root device/snapshot ID, build lineage, guest compatibility identity, architecture, minimum disk and release catalog manifest path/hash. Preserve the actual source AMI and source region when copying. Require `amd64`, HVM and EBS, public launch permission, available state, and a clean unencrypted backing snapshot for publication.

The same manifest and catalog metadata can be read by RC and stable installs. AWS-observable facts must match the release-backed descriptor. Publisher tags are useful for build inventory. The customer path uses observable AWS metadata and verified release provenance.

## Qualification record

- `schemaVersion`: Required integer equal to `1`.
- `candidate`: Required RC tag, source commit, GitHub public release ID and required asset IDs and SHA-256 values. Bind the manifest checksum and its immutable controller digest, template checksum and all four exact hosted object versions and four regional AMI identities.
- `regionalLaunches`: Exactly four results for the manifest regions. Each records matching AMI/owner, a passing independent public boot, encrypted launched volume, cross-account hosted-template validation and verified disposable cleanup.
- `freshInstall`: Required passing console quick-create fresh-account install through signed-in browser IDE in `us-east-2`, with private HTTPS, protected setup and anonymous package retrieval verified, and a recorded CLI change-set and resume exercise.
- `gateResults`: Required passing references to the existing applicable validation gates. Include explicit coverage notes for AWS equivalents and any provider-specific steps covered by separate UTM regression.
- `cleanup`: Required verified disposition for disposable resources, distinct from retained release assets and the shared demo.

The record is approved in protected main after candidate qualification. It contains minimal public outcomes. Keep raw logs, AWS validation account IDs, session details, endpoints and resource inventory private. Stable independently validates the record's commit, source, identities, contents and live availability.

## Hosted template location

- Required bucket/region, content-addressed object key, nonempty S3 version ID and versioned regional HTTPS URL. The SHA-256 equals the GitHub template asset's checksum in all four regions.
- Quick-create URL carries the same console region and percent-encoded template URL. Only nonsecret defaults may appear. Each published object version is readable anonymously. Object listing, mutation and nonrelease reads require separate publisher access.
- RC and promoted stable reference the same object versions. Retention permissions protect the published versions from accidental deletion.

## Deployment state

- Required identity combines one AWS partition/account, region, stack name and CloudFormation stack ID once initialized.
- Selected tag and immutable payload checksums are recorded once. A resume requires the same account, region, deployment identity and payload. Version changes to existing installations follow the upgrade procedure.
- Local phase metadata identifies the next review/verification to perform. CloudFormation stack, event and change set IDs, ECS/SSM observations and persisted setup activation are authoritative about side effects.
- Only references and public trust material cross into CloudFormation parameters and outputs. Store private local files with operator-only permissions and publish no deployment metadata or working directories.

Transitions: preflight → reviewed foundation → protected DB TLS populated → reviewed database and setup → database ready → protected setup active → setup stopped and activation verified → reviewed controller activation → first-workspace handoff. Reconcile an interrupted phase before continuing. Create and review a fresh change set after stack, identity or inputs change. Retained EBS and Secrets Manager resource IDs are tracked across lifecycle changes.

## Installation trust material

- Browser root and server certificate are unique per guided installation. The leaf covers management and the IDE, preview and repair wildcard names. The trust-import artifact is public PEM with a verified fingerprint. Keep the issuer key in protected operator custody and the server leaf/key in the existing installation certificate store.
- PostgreSQL trust is distinct: a verified database CA and matching `postgres.<dns_suffix>` server certificate/key. Store database credentials and TLS private material only in scoped Secrets Manager values or protected local provisioning files.
- The temporary setup endpoint and internal guest mTLS keep their existing independent authority and trust stores.

## Catalog relationship

A persisted catalog entry associates the image with the deployment account for workspace authorization and ownership. Official provenance separately identifies the publisher, region, exact AMI and immutable build. The existing import validation and exact profile selection determine launch eligibility. Legacy customer-owned images retain their current semantics. Retained workspaces preserve their captured image identity. An upgrade must retain the required permissions and provenance for existing workloads per the recovery and maintenance contract.
