# Codebase review corrections and release

The user requested a codebase-level review and authorized release once no blocking issue remains. The prior test-first instruction remains in force. Source starts at f17fcf1a; the original checkout contains the identical reviewed patch.

## Required corrections

1. Paseo must accept the actual current Environment catalog, including both boolean values of clientToolAccessEnabled, while accepting the documented v0.1.23 id/name-only catalog. Keep strict validation and duplicate-ID rejection.
2. Normal Runtime shutdown must drain the OAuth credential manager and retry a pending rotated-secret commit before its required dependencies are closed. Recovered storage must persist the new epoch; continuing failure must be surfaced. Preserve startup rollback and existing lifecycle ownership.
3. Auxiliary/opaque Original Destination requests must use the immutable actual client-selected origin, including scheme, host, port and request path/query, regardless of the model Route's destination mode. A canonical Client Flow identity selects protocol behavior and cannot select the outgoing credential destination. Preserve target-access policy, TLS verification and unknown-path rejection.
4. Every raw cleartext upstream socket and blocking body operation must belong to the proxy action lifecycle. Downstream cancellation and Runtime shutdown must interrupt reads/writes, join callbacks/workers, complete audit and release egress. Preserve normal streaming and avoid a parallel proxy implementation.
5. Existing connections cannot retain authority to start new operations after Capture finish/expiry, Manual Capture revocation/credential rotation or originating Runtime User/Login Session revocation. Authority failure is fail-closed. Blind tunnels need cancellation ownership because they have no inner request admission. Reuse the authoritative Capture/session state and existing revocation machinery; do not rely only on a retained connection snapshot or heartbeat expiry. Preserve established distinctions between revoking authority and merely changing the route for future requests.

## Interfaces and verification

Tests use production CLI/plugin parsing, Runtime lifecycle, real proxy CONNECT/HTTP sockets and real Capture/session managers with temporary state and synthetic external peers. The review's closed-loop reproductions establish the initial RED; permanent regressions must independently show RED before the owning change. Do not inspect real credential payloads, send model requests, reset installed apps or change OS trust/proxy settings.

The codebase reviews are in .superpowers/review/2026-10-16-codebase-release. They cover representative deep paths across core, state and client/UI modules and record their limits; no claim of an exhaustive proof of every line is made. Fix each confirmed blocker and independently review its final implementation. Clarify stale ADR0021 status without rewriting historical decisions or claiming a new migration.

## Release

Proposed next identity is 0.1.29+31; v0.1.29 is currently vacant and latest public release is v0.1.28. Reconfirm before freezing and publishing. Preserve schema/data/account/trust compatibility; this is not a feature or dependency-upgrade project.

Use the existing validation, protected macOS signing/notarization/installed-evidence and Linux publication workflows, plus the existing Homebrew tap path. No protection bypass, reviewer impersonation or unqualified asset publication. Release authorization is already granted; normal source integration/push/PR/tag/publication need no repeated confirmation after qualifications pass. Actual designated-reviewer gates remain authoritative. A software release does not authorize restarting the user's app or deploying the production Runtime Server.

Existing exact-source Go evidence may be reused until relevant source changes invalidate it. Final candidate needs Go/race/native/security, Flutter/browser/native-package, release-tooling, data-upgrade/restore and bundled-helper evidence according to current repository workflows. Preserve first failures and actual job/source/artifact IDs; do not duplicate remote jobs when observation times out.
