# Validation and engineering defaults

**Status:** Explicit engineering proposals under Q155, for review with the [design readback](design-readback.md). Accepted product/security contracts take precedence. These documents are not a claim that tests have passed or authorization to implement.

## Authority and closure

All interview responses Q1–Q155 are captured in the [decision record](shiftbox-design-decisions.md). Later answers supersede earlier proposals. This series presents the effective design; historical questions and old research are evidence, not active requirements.

The user delegated internal implementation choices within the agreed boundaries. Paths, libraries, endpoint names, utility packaging and tunable defaults can be selected and validated without reopening every question. A change to data loss, trust, required operator burden, supported scope or deployment capability returns for a product decision. Final readback confirmation and implementation authorization remain separate gates.

## Defaults and tunables

“Accepted” means a user decision. “Engineering default” means a concrete starting proposal, visible in operator documentation and subject to validation. Values are not performance claims.

| Setting | Initial contract/default | Status and limits |
| --- | --- | --- |
| Internal leaf/key replacement | Every 4 hours; 8-hour validity | Accepted Q150; issuer expiry caps leaf validity; never accept expired identity |
| Internal CA | Stable during ordinary leaf rotations | Accepted; track expiry/compromise separately, not an immortal root |
| External OIDC authorization interval | 4 hours; positive finite admin-configurable value | Engineering default for amended Q152; not a universal industry standard or disable-detection SLA |
| Web certificate notice | 30 days before expiry | Accepted; invalid/expired state remains visible; no production HTTP fallback |
| Enrollment grant | 15 minutes, created immediately before authorized start/recovery | Engineering default; validate against amd64 boot times, expose bounded configuration; expiration requires a new verified attempt |
| Guest desired-state poll | 30 seconds, randomized by up to 20% | Engineering default; not an immediate revocation mechanism or scale-tested throughput claim |
| Activity sampling/freshness | 30-second sample; active report becomes stale after 90 seconds | Engineering default; missing-poll time still counts from controller-observed activity/contact, not a new idle reset |
| CPU activity monitor | More than 5% of assigned CPU over a 2-minute window | Engineering default, admin-configurable; validate guest accounting and document false activity |
| Startup/known-outage grace | 10 minutes after reconciliation is ready | Engineering default; visible/configurable; elapsed outage time still counts toward retention |
| Retention deletion shutdown timeout | 60 seconds before provider-enforced stop/delete | Engineering default; only after accepted explicit policy/action, final checks and warnings |
| Automatic workspace deletion | Disabled until configured | Accepted; no invented default lifetime or storage-pressure deletion |
| Maintenance deadline | Explicit administrator selection | Accepted; no acknowledgement requirement; one maintenance VM at a time initially is an engineering default subject to capacity |
| Audit / dismissed notice history | 90 / 30 days | Engineering defaults, admin-configurable; protect current authorization, active work and provenance outside history cleanup |
| Initial PG namespace names | `shiftbox` and `dex` in one database | Engineering names; separate roles/search paths and least authority remain mandatory |
| CLI name | `shiftbox`; retain `secrets get-secret <name>` behavior | Engineering naming after rebrand, not a statement that a binary exists |
| Retained data layout | VM-managed project/home/application directories bound to effective container paths | Engineering mapping; actual user's home is preserved, not assumed `/home/vscode` |
| Phase-one startup | Manual native Mac launch | Accepted; no unattended pre-login/sleep/logout guarantee |
| Backup format | Versioned manifest + authenticated streaming encrypted archive; age is proposed library | Engineering selection; full validation, independent password and recoverable keys are required |

Timer settings are different clocks. Web authorization is not VM inactivity; mTLS validity is not an OAuth token lifetime; polling is not proof of human activity. No additional browser-idle logout behavior is silently introduced by the OIDC default.

The active-revocation path must close affected controller/proxy connections promptly when authorization changes; it must not wait for a poll or certificate expiry. No network design can retract a value already copied by a guest.

## External identity default rationale

There is no single industry-standard OIDC interval. NIST separates overall reauthentication from inactivity; its AAL2 guidance recommends an overall timeout of at most 24 hours and inactivity at most one hour. ShiftBox phase one defers MFA, so these figures do not establish AAL2 compliance. Keycloak documents a 10-hour SSO maximum default, while Okta documentation discusses a two-hour **idle** default. These clocks cannot be substituted for ShiftBox's upstream authorization interval. [NIST SP 800-63B-4](https://pages.nist.gov/800-63-4/sp800-63b/aal/), [Keycloak sessions](https://www.keycloak.org/docs/latest/server_admin/), [Okta idle guidance](https://help.okta.com/oie/en-us/content/topics/security/healthinsight/session-lifetime.htm)

Keep four hours as a conservative product default, configurable upward or downward by the administrator. Longer values extend the possible period before a change at the upstream provider affects access. Require actual successful authorization through that provider before the deadline; a refreshed token or broker-local session does not alone reset it. Upstream SSO may satisfy authorization without a password prompt. Validate each supported connector; generic OIDC is not an account-enabled assertion or immediate offboarding mechanism. Explicit ShiftBox disablement remains immediate. [OIDC authorization](https://openid.net/specs/openid-connect-core-1_0.html#AuthRequest), [refresh semantics](https://openid.net/specs/openid-connect-core-1_0.html#RefreshTokenResponse)

## Mandatory release validation

| Gate | Evidence required before a supported release |
| --- | --- |
| V1 Exact platform/provider | Build pinned Ubuntu 24.04 Minimal amd64 with selected Packer/UTM fork; import, boot, clone with distinct identities/disks, stop and verify full owned-resource deletion |
| V2 Enrollment delivery | Replace only designated recovery media while preserving every workspace drive; prove short/long shutdown, grant consumption/replay rejection, generation binding and cleanup |
| V3 Certificate lifecycle | Prove 4h/8h rotation, overlap, clock errors, controller restart, long-offline recovery, expired CA trust, revoked/deleted/cloned identities and tunnel/resumption revocation |
| V4 Guest compatibility | Recover and update N−1; block advancement with stopped/unknown/failed old guests; prove rollback without irreversible local state breakage or repeated bad-artifact loops |
| V5 Provider networking | Validate effective controls before advertising capabilities; unsupported restrictions remain unavailable; explicit DNS/Git/API exceptions cannot silently widen |
| V6 Work/data contract | Retain project/home/designated app files across reboot/start/rebuild; trusted repair works after configuration/build/hook/IDE failures without running broken project automation |
| V7 Reconciliation/deletion | Inject lost responses, process crashes, duplicate commands, concurrent start/delete, capacity pressure and disk exhaustion; no duplicate destructive effects or premature reservation release |
| V8 Retention/maintenance | Fresh activity cancels before deletion commit; missing polls count; outage grace works; upgrades never force-kill unsafe work; authorized deletion can escalate and verifies cleanup |
| V9 Identity/bootstrap | Restricted setup gate, stable initial-owner/password transfer, fresh sign-in, no stale fallback; scoped schemas/search paths; external authorization deadline and local offboarding |
| V10 Backup/restore | Coordinated app/Dex/key archive, wrong-password/key/truncation failures, supplied-key recovery and original-Mac empty-replacement-PG restore; restored grants/sessions stay gated |
| V11 Application authorization | Owner/admin permission separation, template sharing/revocation/archival, three-scope precedence/empty fallback, admin connection retains owner context, no management secret redisplay |
| V12 Browser/tunnel boundary | Hostile preview origin/CSRF/cookie tests; strip management credentials; WebSocket admission and existing-session revocation; no public previews |
| V13 OAuth | Encrypted token restart persistence, coordinated per-user refresh, revoked grant/reconnect and no false promise of updating copied developer-tool tokens |
| V14 Operability/accessibility | Mantine UI static export served by Go, hydration/styles, keyboard navigation and focus handling; setup error recovery, expiry notices, read/dismiss history, keyboard paths and readable failures; documented prerequisites and irreversible-update recovery |

These are functional/security tests. Load/scaling tests remain deferred until the product is fully implemented, as requested. No 100-workspace or concurrent-start performance target was adopted.

## Engineering work remaining after confirmation

Mantine is the selected UI component library; pin compatible Mantine/React/Next.js versions and validate the static build. Select/pin other libraries, supported PostgreSQL/client-tool versions, browser IDE distribution/license-compatible packaging, Dev Container/Docker/Node versions and image tooling. Define provider APIs, operation schemas, channel framing, seed-media format and current-token exposure interface. Build release manifests, migration/recovery commands, telemetry redaction and UI controls. These are tasks within the accepted contract, not evidence that the implementation already exists.

The default token exposure interface must not revive the rejected separate broker product or bundled `gh` launcher. Developer-managed applications may retrieve/use the current owner-authorized token; copied old values may fail after refresh. No app private key or OAuth refresh token becomes a developer application secret.

## Deferred or excluded scope

- Phase II: EC2, Linux/container production hosting, optional MFA, application encryption-key rotation.
- Later/uncommitted: extra IDEs, custom-role editor, template purge/ownership transfer, populated-demo migration, remote UTM workers, arbitrary runtime plugins.
- Outside phase one: HA/shared multi-organization SaaS, Windows hosting, platform-managed Compose, Alpine/musl containers, public previews/workspace sharing, automatic workspace-data backup/migration or platform-OS replacement.
- Explicitly not product features: PR management, bundled developer `gh`, managed LFS/submodule/fork workflows, central hosted GitHub integration, external notification services, a production Next/Node server.

No weak fallback is permitted merely to pass a gate. If the exact UTM image/recovery route or another integration cannot satisfy the contract, document the evidence and seek a bounded design change rather than silently relaxing identity checks, preservation or capability claims.
