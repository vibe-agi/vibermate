# macOS arm64 Packaged-App Acceptance

`cmd/vibermate-acceptance` is the opt-in packaged-product acceptance runner.
It exercises the selected `ViberMate.app`, the `vibermated` and `vibermate`
members of that same bundle, the production Desktop composition, and one
fixed real Agent executable. It is not a unit-test fixture, an installer test,
or evidence that every supported product capability is assembled.

## Current boundary

The runner accepts exactly one fixed client:

- Claude Code 2.1.220 on Darwin arm64; or
- Codex CLI 0.145.0 on Darwin arm64.

The built-in client catalog defines the required executable evidence. A
different client build can still be launched by the product under its normal
recognition rules, but it cannot produce this fixed-client acceptance report.

Every run also requires:

- an absolute path to a packaged `ViberMate.app`;
- a canonical Environment ID;
- a private report path when evidence is to be retained; and
- a clean, fully committed source checkout for independently verified
  provenance.

The runner has no provider-origin, model, account, credential, or secret-value
argument. Deterministic assembly evidence must not acquire those authorities
from ambient environment variables or developer state.

## Artifact and report provenance

The Desktop build embeds `vibermate-build-manifest.json`. The manifest binds:

- the full Git revision, commit time, and clean-tree state;
- pinned Go, Node, Rust, Cargo, pnpm, and Tauri toolchains;
- Desktop and sidecar build profiles and target triple;
- configuration-input digests; and
- the packaged daemon and launcher digests.

The runner rejects mixed source identities, dirty builds, substituted
sidecars, malformed manifests, unknown manifest fields, and daemon or launcher
paths that are not direct members of the selected App bundle.

The current report contract is the only accepted contract. Reports are
atomically written as regular files with mode `0600`; the verifier rejects
links, oversized files, unknown fields, stale revisions, wrong modes, and any
artifact or configuration digest mismatch. The verifier receives the trusted
source root, App, acceptance executable, fixed client, expected mode, schema,
and full revision independently. A report cannot choose the bytes used to
verify itself.

Reports contain no prompt, response body, headers, tool arguments, thread ID,
credential reference, token, or secret value.

Build from a clean frozen commit with the pinned toolchains:

```sh
RUSTUP_TOOLCHAIN=1.88.0 fnm exec --using=22.23.1 -- \
  pnpm --dir ui/desktop install --frozen-lockfile
RUSTUP_TOOLCHAIN=1.88.0 fnm exec --using=22.23.1 -- \
  pnpm --dir ui/desktop bundle:development
go build -buildvcs=true -trimpath \
  -o /private/tmp/vibermate-acceptance \
  ./cmd/vibermate-acceptance
go build -buildvcs=true -trimpath \
  -o /private/tmp/vibermate-acceptance-verify \
  ./cmd/vibermate-acceptance-verify
```

The development bundle is not a release package. Signing, notarization,
distribution, system trust-store mutation, and native secret protection are
separate gates.

## Deterministic sequence

`--deterministic-only` proves the current assembly without provider
credentials or provider traffic. The required checks are:

1. bind the clean source, App bundle, packaged members, toolchains, fixed
   client, and report to one provenance identity;
2. acquire exclusive Desktop generation ownership;
3. start the packaged native shell, restore its main navigation, and reach the
   packaged daemon through the private bootstrap channel;
4. create an Environment draft, compute its impact preview, and atomically
   publish revision 1 with a canonical digest;
5. invoke the packaged launcher as `vibermate run --env <environment> -- ...`
   and persist one typed managed-Capture assignment to that exact Environment
   revision and launch-authority boundary;
6. drain the first daemon generation and remove its owned discovery record;
7. start a new incarnation on the same private SQLite store;
8. recover the exact Environment revision/digest and Capture assignment from
   SQLite; and
9. drain the recovered generation cleanly.

Example:

```sh
/private/tmp/vibermate-acceptance \
  --desktop-app=/absolute/path/to/ViberMate.app \
  --claude=/absolute/path/to/the/fixed/claude \
  --environment-id=assembly-001 \
  --deterministic-only \
  --report=/absolute/private/path/deterministic.json
```

Use `--codex=/absolute/path/to/the/fixed/codex` instead of `--claude` for the
fixed Codex path. Supplying both or neither is rejected.

## Provider-account continuation

Without `--deterministic-only`, the runner first completes the deterministic
phase, then starts a second fresh private runtime and independently proves
Environment publication and Capture assignment again. It currently records
`provider-account` as `blocked`, drains without provider traffic, writes the
truthful blocked report, and exits with status 3.

This is deliberate. The current Environment-first runtime does not yet
assemble the typed ProviderAccount authority needed for managed provider
traffic. The runner must not recreate the retired Access/secret path or
fabricate a credentialed success. A successful credentialed acceptance can be
defined only after ProviderAccount selection, credential ownership, final
header injection, attempt evidence, and commit-safe retry are part of the same
production composition.

## What this evidence does not prove

Even a passing deterministic report does not prove:

- a real provider response or credentialed account flow;
- system Root installation or removal;
- arbitrary Agent versions or application-wide capture;
- Server/LAN operation;
- plugin execution, Language Bridge, quality evaluation, or account failover;
- signed/notarized packaging, install, upgrade, or uninstall; or
- Preview or Release readiness.

Those statements remain blocked until their own production path and evidence
exist.
