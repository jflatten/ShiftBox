# Planning Quality Checklist

This checklist describes documentation validation only. Runtime gates remain unchecked in tasks.md and must acquire real evidence during implementation.

- [x] Settled Distribution embedding, single process, public anonymous scope and MIT/Apache obligations retained without reopening choices.
- [x] Source references distinguish proven APIs from unproven runtime behavior; exact patch surfaces and toolchain compatibility gates identified.
- [x] Transparent original refs cover prebuilt and actual Dockerfile builder, without devcontainer.json/Dockerfile rewrites or TLS interception.
- [x] All outbound requests, constructor/token/blob/redirect/DNS paths and per-VM guest authority have explicit security tasks.
- [x] Restricted image-only exception, policy-safe direct fallback and no-retention streaming are explicit; no unrestricted proxy or allowlist introduced.
- [x] Tags revalidate; immutable digests reuse; partial cache and complete stale closure semantics documented.
- [x] Exact persistent warning and recovery versus running-image digest are distinct and tested in plan.
- [x] Hard cache and physical-volume admission, concurrent/temp bytes, active leases, LRU, shared count-once and external writers covered before shipping.
- [x] Administration → Infrastructure route, all settings/actions/status, backend/persistence/permissions/E2E matrix and UI nonhappy states are complete.
- [x] General administrative UI completeness rule added to tracked applicable root AGENTS.md.
- [x] Data model, contracts, source diagram, validation guide and test-first dependency-ordered tasks cohere; meaningful parallel groups are delegated only during execution.
- [x] Configuration migration/activation/path move/restart, backup exclusions, guest compatibility and rollback are covered without reserving a stale migration number.
- [x] License packaging checks include actual linked dependencies and modified source notices; no unqualified legal certification.
- [x] Shared demo-only AWS validation, Terraform, local secret/state handling, preserved existing resources and disposable cleanup specified for later execution.
- [x] Direct/cold/warm byte and actual IDE timing proof excludes Packer preload/guest image/build-cache confounders; no measured speed claim.
- [x] Planning-only scope preserved; unrelated keep-alive changes and reference architecture untouched; exact feature/mvp base verified.

- [x] Formal speckit-analyze performed read-only, authorized remedies applied between passes, and final rerun saved in [analysis.md](../analysis.md) with all 17 FR/SC requirements and 12 UI rows mapped to the simplified tasks/tests.

- [x] All artifacts converge on 24 tasks, six combined validation checks and existing persistence/journal/guest/UI patterns; speculative test permutations, arbitrary new tuning limits and generic subsystems removed.
