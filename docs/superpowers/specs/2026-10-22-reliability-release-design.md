# Reliability and current-client compatibility release

The user authorized an overall repair and release on 2026-10-22 after reviewing the codebase and Web-detail findings. This is implementation of those reviewed repair directions, with no additional approval gate for routine fixes, PR integration, or publishing the verified release.

The baseline is v0.1.35 / b03ebd8be040087d623f7a1b12c340e99994f56f. Include the previously reviewed PR #66 (6995ea291a13505fa14a68666cbfee07bf3accb9). Target v0.1.36, build 38 unless a concurrent published release requires the next unused version.

Requirements are the confirmed findings and source-backed compatibility boundaries in:

- [Whole-codebase review](../../reviews/2026-10-22-codebase-codex-review.md).
- [Web-detail follow-up](../../reviews/2026-10-22-web-detail-followup.md).
- [Codex upstream comparison](../../reviews/2026-10-22-codex-upstream.md).
- [State and delivery review](../../reviews/2026-10-22-state-ui-delivery.md).

Keep the current architecture: one shared Agent transport/control runtime, client-specific launch adapters, service-specific auxiliary read contracts, reliable authority state, and optional bounded recording. Do not introduce a message broker, broad client-path bypass, global direct fallback, or model-request replay as a repair.

Repair all ten confirmed findings. Reproduce and resolve the reported summary 409 if a causal code defect is identified; otherwise improve phase-specific diagnosis and bounded recovery without suppressing visibility checks or claiming an unproven live cause. Test the known network cancellation/budget risks and repair demonstrated failures. Complete the reviewed stable Codex adapter gaps and add current native acceptance while preserving Claude/generic-client support.

Required safety and compatibility invariants:

- Content cursors remain opaque, bounded to 2048 characters, and are transmitted unchanged. Preserve current signed two-component cursors and intentional older single-component compatibility. Server signature/source/Exchange/visibility checks stay authoritative.
- Run completion never fabricates Exchange success or saved content. Old-selection transient errors cannot invalidate a newer selection; actual privacy/source conflicts still invalidate retained evidence.
- Logs/content/usage projections are droppable; accounts, configuration, permissions, approvals and native one-use delivery facts remain reliable.
- No-work routine retention must avoid full archive rewrites; physical deletion still requires durable visibility fencing and cleanup certification. Failed maintenance must retain its typed cause and recover only through verified cleanup.
- Native client credentials and enterprise requirements retain their identity and semantics. Do not manufacture a successful empty policy or import managed inference identity into original workspace requirements.
- Keep selected egress and credential scopes immutable per operation. Do not fall back to direct, change accounts, or replay a committed model request after a network failure.
- Go/kernel toolchains and release receipts must agree on a patched version at least Go 1.26.10. Current stable Codex acceptance pins 0.163.0; main-only requirements are separately tracked.

Tests first at existing public seams: production control HTTP → Flutter decode → cursor continuation; public workbench controller/widgets; native isolated Codex configuration/bootstrap; runtime recording/inspection APIs; shared egress dialers; build/package verification. The user already requested TDD and authorized these repairs. Preserve RED evidence and verify GREEN after each bounded change.

Release only after task reviews, whole-branch review, appropriate local/race/native checks, exact-source CI, and artifact signing/notarization/integrity verification. Publish Desktop/CLI and Linux Runtime/Web artifacts consistently; update distribution metadata through the repository's established process. Keep evidence and report the actual released artifacts and any deployment-specific limit honestly.
