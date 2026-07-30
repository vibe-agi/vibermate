# M0 Assembly Acceptance

`cmd/vibermate-acceptance` is the opt-in macOS arm64 runner for the
packaged M0 Desktop assembly. It exercises production entrypoints and the real
fixed Agent executable. It is not a fixture server, an installer test, or a
replacement for deterministic repository gates.

## Fixed boundary

The runner requires:

- one packaged `VibeMate.app`;
- the `vibermated` and `vibermate` executables that are direct members of that
  same App bundle;
- an absolute Claude Code 2.1.220 executable whose invocation label and
  SHA-256 match the fixed Darwin arm64 release;
- one provider HTTPS origin, fixed model, Access ID, and logical `SecretRef`;
- a private path for the JSON evidence report.

The fixed Claude executable SHA-256 is
`8addc857f3fe64d5a0368af9ee50321b50afb4a6918ba3ef018ab84f5dbbe081`.
The default provider route for this acceptance slice is
`https://api.example.com/v1` with model `gpt-5.6-sol`.

The runner never accepts a secret value. It removes ambient Anthropic,
alternate-provider, Claude credential, and OpenAI credential variables from
the captured child environment. A non-secret placeholder forces the Agent to
enter the VibeMate proxy. The active provider value is resolved by the
runtime's selected SecretStore only after the offline-egress lease is granted.

The ordinary M0 build intentionally uses the development file SecretStore:

```text
<user config>/io.vibermate.desktop/development-secrets/store.json
```

Its contents are plaintext-equivalent. Use only a development credential. No
Keychain or other native secret backend is built, selected, or tested in M0.
Credential control exposes state and revision metadata but never reads a value
to render status and never returns a `SecretRef` or secret value.

## Frozen artifact provenance

`prepare:desktop` generates an ignored
`vibermate-build-manifest.json` and Tauri embeds it in the App resources. The
manifest records:

- Git revision, commit time, and dirty state;
- Go, Node, Rust, Cargo, pnpm, and Tauri CLI versions used by the App build;
- Desktop profile, sidecar profile, and target triple;
- SHA-256 values for the Go, Node, Rust, Cargo, lock, and Tauri configuration
  inputs;
- SHA-256 values for both packaged Go sidecars.

The acceptance runner rejects:

- a dirty build;
- mixed Git source identities among the runner, daemon, launcher, and App
  manifest;
- a daemon or launcher that is not the selected App's direct member;
- a manifest sidecar digest that differs from the packaged member;
- an unpinned build or acceptance-host toolchain;
- a missing, malformed, oversized, or unknown-field manifest.

The v3 report records the deterministic App-bundle manifest digest, individual
artifact digests, build and host toolchains, build profiles, configuration
digests, and redacted run configuration. It is atomically replaced with mode
`0600`. It contains no prompt, response body, header, tool arguments, or secret
value.

Build from a clean frozen commit with the pinned toolchains:

```text
RUSTUP_TOOLCHAIN=1.88.0 fnm exec --using=22.23.1 -- \
  pnpm --dir ui/desktop install --frozen-lockfile
RUSTUP_TOOLCHAIN=1.88.0 fnm exec --using=22.23.1 -- \
  pnpm --dir ui/desktop bundle:development
go build -buildvcs=true -trimpath \
  -o /private/tmp/vibermate-acceptance \
  ./cmd/vibermate-acceptance
```

The development bundle is not a release package. Signing, notarization,
native secret protection, installation, and distribution remain outside M0.
The final artifact must be built from a standalone clean checkout. Some linked
Git worktrees omit Go `vcs.*` build settings even when the build requests VCS
stamping; the runner rejects such an artifact instead of inventing provenance.

## Deterministic sequence

`--deterministic-only` replaces the configured logical secret reference with a
cryptographically random, run-local missing reference. This prevents an
existing development credential from turning the no-send checks into provider
traffic.

The deterministic sequence verifies:

1. the fixed Claude executable identity;
2. exclusive Desktop generation ownership;
3. the packaged App starts with an isolated temporary user directory,
   exchanges the native-shell bootstrap, publishes readiness, and gracefully
   drains its packaged sidecar;
4. the packaged daemon starts from an inherited bootstrap descriptor with
   complete proxy and control routes;
5. one executable Access commits as revision 1;
6. fixed Claude reaches the proxy while egress is held and queues approved
   original-origin control traffic with zero active egress;
7. Resume performs TLS-only, no-credential probes for the exact queued
   original origin and frozen provider target before release;
8. the semantic request reaches the intentionally missing development
   credential boundary, records `provider_credential_unavailable`, and does not
   send provider HTTP traffic;
9. body-free ConnectionEvent evidence correlates the configured Agent ingress,
   observed SNI, MITM decision, selected provider host, and credential-binding
   identifier;
10. daemon `SIGINT` drains the Host and removes owned discovery;
11. a new incarnation reopens SQLite and rejects `expectedRevision=0`, proving
    revision 1 recovery;
12. another fixed-Claude request remains queued behind Hold when the daemon is
    killed;
13. daemon `SIGKILL` terminates that request without a completion marker,
    releases kernel generation ownership, and leaves no resurrected in-memory
    queue;
14. the replacement generation recovers revision 1 and appends exactly one
    `daemon_restarted` terminal to the interrupted ConnectionEvent;
15. the final generation drains cleanly.

Example:

```text
/private/tmp/vibermate-acceptance \
  --desktop-app=/absolute/path/to/VibeMate.app \
  --claude=/Users/null/.local/bin/claude \
  --deterministic-only \
  --report=/absolute/private/path/m0-deterministic.json
```

## Credentialed continuation

Before the credentialed run, use the same development-profile App to apply the
acceptance Access and save its provider key:

1. set Access ID to `assembly-001`;
2. set the provider origin to `https://api.example.com/v1`;
3. set the fixed model to `gpt-5.6-sol`;
4. apply the Access at the currently loaded revision;
5. save the provider credential once and confirm a nonzero secret revision.

The default logical reference is
`secret://provider/assembly-001-account`. A different Access ID or logical
reference may be supplied explicitly, but no secret value may be passed on the
command line.

The credentialed continuation additionally verifies:

1. fixed Claude completes an unheld provider reply with a trusted assistant
   marker and at least one incremental content delta;
2. a new request queues while Hold is active, sends nothing before Resume, and
   returns a trusted marker with at least two deltas after the exact route probe;
3. a real `TodoWrite` intent becomes durable pending approval without raw
   arguments, and neither the tool block nor completion marker reaches Claude
   before `allow-once`;
4. signaling captured Claude after its first streamed delta terminates the
   child within the bound while the shared runtime remains ready and all hold
   ownership converges;
5. the final packaged daemon drains cleanly.

Example:

```text
/private/tmp/vibermate-acceptance \
  --desktop-app=/absolute/path/to/VibeMate.app \
  --claude=/Users/null/.local/bin/claude \
  --report=/absolute/private/path/m0-credentialed.json
```

Exit status `0` means every selected check passed. Exit status `3` means the
deterministic sequence passed but configured credential metadata was absent.
Other nonzero statuses are failures.

## Evidence boundary

A successful M0 report proves only this fixed macOS arm64 Desktop slice, fixed
Claude release, configured relay/model, and captured run. It does not prove
physical sleep or network removal, power-loss durability, arbitrary Agent or
provider compatibility, reverse protocol translation, exact JA3/JA4 or HTTP/2
fingerprint parity, Root installation, signed/notarized distribution, Server,
Windows/Linux, or Preview/Release readiness.
