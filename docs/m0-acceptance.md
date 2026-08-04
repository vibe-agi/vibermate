# Initial macOS arm64 Packaged-App Acceptance (M0)

`cmd/vibermate-acceptance` is the opt-in macOS arm64 runner for the
initial packaged Desktop milestone, whose internal milestone code is M0. It
exercises production entrypoints and the real fixed Agent executable. It is not
a fixture server, an installer test, or a replacement for deterministic
repository gates.

## Fixed boundary

The runner requires:

- one packaged `VibeMate.app`;
- the `vibermated` and `vibermate` executables that are direct members of that
  same App bundle;
- exactly one absolute fixed-client executable:
  - Claude Code 2.1.220 with its fixed Darwin arm64 native-binary digest; or
  - Codex CLI 0.145.0 with its fixed npm wrapper, package metadata, platform
    package metadata, and Darwin arm64 native-child digests;
- one provider origin, fixed model, Access ID, and logical `SecretRef`;
- a private path for the JSON evidence report.

The fixed Claude entrypoint SHA-256 is
`8addc857f3fe64d5a0368af9ee50321b50afb4a6918ba3ef018ab84f5dbbe081`.
The Codex identity is compound evidence rather than a version string or one
wrapper hash. The built-in client catalog is the authority for every required
artifact digest and the fixed `ssl_cert_file` launch recipe. Other executable
versions remain valid generic proxy clients, but they cannot be used to
produce this fixed-client acceptance evidence.
The default provider route for this acceptance slice is
`http://127.0.0.1:23333/v1` with model `dashscope:glm-5`.

Remote provider origins remain HTTPS-only with strict system-root validation.
The default exercises the design's narrower development exception: cleartext
is accepted only for a literal loopback IP, is forced through Direct egress,
does not use ambient proxies, and verifies the connected TCP peer before any
authenticated HTTP byte is written. `localhost`, LAN, private-CIDR, and public
HTTP origins are rejected.

The runner never accepts a secret value. It removes ambient Anthropic,
alternate-provider, Claude, Codex, and OpenAI credential/base-URL variables,
plus conflicting CA inputs, from the captured child environment. A non-secret
client placeholder forces the selected Agent to enter the VibeMate proxy. A
Codex run also receives a private run-owned `CODEX_HOME`, ignores user config
and rules, and receives prompts only over standard input. The active provider
value is resolved by the runtime's selected SecretStore only after the
offline-egress lease is granted.

The fixed Codex acceptance uses two deliberately separate invocation
profiles. One dedicated fallback process leaves the client's supported
WebSocket negotiation enabled and proves the local bounded 426 selects an HTTP
connection inside the same CaptureRun. Each independent semantic process uses
the client's supported explicit Responses HTTP provider configuration, with
WebSocket disabled, so session-local negotiation cannot race the actual
semantic assertion. Both profiles still traverse the authenticated launcher,
proxy, exact operation catalog, one Access snapshot, Exchange, and controlled
egress. This is an acceptance invocation choice, not a production client
branch, Access setting, provider bypass, or claim of successful WebSocket
semantics.

The ordinary build for this initial milestone intentionally uses the
development file SecretStore:

```text
<user config>/io.vibermate.desktop/development-secrets/store.json
```

Its contents are plaintext-equivalent. Use only a development credential.
Neither Keychain nor another native secret backend is built, selected, or
tested in this milestone.
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

The current v6 report records the selected client identity and typed compound
adapter evidence, deterministic App-bundle manifest digest, individual artifact
digests, build and host toolchains, build profiles, configuration digests, and
redacted run configuration. It is atomically replaced with mode `0600`. It
contains no prompt, response body, header, tool arguments, thread ID, or secret
value. Historical v5 reports retain their frozen schema and check set, but they
are accepted only when the verifier caller explicitly requests v5.

Current v6 verification does not trust artifact paths or digests merely because
they occur in the report. The caller independently supplies the source checkout,
selected App, acceptance executable, and fixed-client entrypoint. The verifier
requires a clean Git top-level checkout at the expected full revision and commit
time, hashes the actual App and each executable, parses the actual v2 build
manifest, and hashes every frozen configuration input from that checkout. A
missing coordinate, substituted artifact, dirty checkout, v1 manifest, stale
revision, or digest mismatch fails the gate.

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
native secret protection, installation, and distribution remain outside this
initial milestone.
The final artifact must be built from a standalone clean checkout. Some linked
Git worktrees omit Go `vcs.*` build settings even when the build requests VCS
stamping; the runner rejects such an artifact instead of inventing provenance.

## Deterministic sequence

`--deterministic-only` replaces the configured logical secret reference with a
cryptographically random, run-local missing reference. This prevents an
existing development credential from turning the no-send checks into provider
traffic.

The deterministic sequence verifies:

1. the selected fixed-client compound executable identity;
2. exclusive Desktop generation ownership;
3. the packaged App starts with an isolated temporary user directory,
   exchanges the native-shell bootstrap, publishes readiness, and gracefully
   drains its packaged sidecar;
4. the packaged daemon starts from an inherited bootstrap descriptor with
   complete proxy and control routes;
5. one executable Access commits as revision 1;
6. an explicit exact-host-and-port connection rule is committed for the fixed
   client origin while the default remains `ask`;
7. the fixed client reaches the exact configured ingress while egress is held;
   Claude queues approved original-origin control traffic, while fixed Codex
   independently produces the bounded local 426-to-HTTP proxy audit before
   queuing the frozen provider target with zero active egress, surfaces HTTP
   426 for the rejected WebSocket request, and records
   `provider_credential_unavailable` for that Exchange;
8. Resume performs no-credential probes for every queued frozen target before
   release; strict HTTPS targets complete TLS, while the literal-loopback
   cleartext exception completes an exact TCP peer check;
9. the semantic request reaches the intentionally missing development
   credential boundary, records `provider_credential_unavailable`, and does not
   send provider HTTP traffic;
10. body-free ConnectionEvent evidence binds the verified Agent ingress,
    observed SNI, MITM decision, and explicit connection-policy rule to the
    client origin. Provider route and credential facts remain request-level
    evidence and are deliberately absent from the connection timeline;
11. daemon `SIGINT` drains the Host and removes owned discovery;
12. a new incarnation reopens SQLite and rejects `expectedRevision=0`, proving
    revision 1 recovery;
13. another fixed-client request remains queued behind Hold when the daemon is
    killed;
14. daemon `SIGKILL` terminates that request without a completion marker,
    releases kernel generation ownership, and leaves no resurrected in-memory
    queue;
15. the replacement generation recovers revision 1 and appends exactly one
    `daemon_restarted` terminal to the interrupted ConnectionEvent;
16. the final generation drains cleanly.

Example:

```text
/private/tmp/vibermate-acceptance \
  --desktop-app=/absolute/path/to/VibeMate.app \
  --claude=/Users/null/.local/bin/claude \
  --deterministic-only \
  --report=/absolute/private/path/m0-deterministic.json
```

Use `--codex=/absolute/path/to/the/fixed/codex` instead of `--claude` to
select the fixed Codex vertical. Supplying both or neither is rejected.

## Credentialed continuation

Before the credentialed run, use the same development-profile App to apply the
acceptance Access and save its provider key:

1. set Access ID to `assembly-001`;
2. set the provider origin to `http://127.0.0.1:23333/v1`;
3. set the fixed model to `dashscope:glm-5`;
4. apply the Access at the currently loaded revision;
5. save the provider credential once and confirm a nonzero secret revision.

The default logical reference is
`secret://provider/assembly-001-account`. A different Access ID or logical
reference may be supplied explicitly, but no secret value may be passed on the
command line.

Full mode first runs the complete deterministic sequence with a fresh,
run-local missing `SecretRef`. After revision 1 recovery succeeds, it applies
revision 2 with the configured `SecretRef` before inspecting credential
metadata or permitting provider HTTP. This keeps every deterministic no-send
claim independent of credentials already present on the host.

The credentialed continuation additionally verifies:

1. revision 2 atomically replaces the run-local missing `SecretRef` with the
   configured logical reference without exposing its value;
2. the fixed client completes an unheld provider reply with a trusted assistant
   marker; Claude exposes at least one incremental content delta, while Codex
   exposes a complete typed JSONL turn bound to a trusted thread identity;
3. fixed Codex additionally completes two distinct successful Exchanges while
   preserving that private thread identity across `exec resume`;
4. a real client tool intent (`Write` for Claude or `exec` for Codex) becomes
   durable pending approval without raw
   arguments; neither the tool block, completion marker, nor bounded proof file
   exists before `allow-once`, and the exact proof file exists afterward;
5. a new request queues while Hold is active, sends nothing before Resume, and
   returns a trusted marker after the exact route probe; Claude also exposes at
   least two content deltas;
6. signaling the captured client during an active streamed Exchange terminates
   the child within the bound while the shared runtime remains ready and all
   hold ownership converges;
7. the final packaged daemon drains cleanly.

Example:

```text
/private/tmp/vibermate-acceptance \
  --desktop-app=/absolute/path/to/VibeMate.app \
  --codex=/absolute/path/to/the/fixed/codex \
  --report=/absolute/private/path/m0-credentialed.json
```

Exit status `0` means every selected check passed. Exit status `3` means the
deterministic sequence passed but configured credential metadata was absent.
Other nonzero statuses are failures.

## Evidence boundary

A successful report proves only this fixed macOS arm64 Desktop slice, the
selected fixed client release, configured provider target/model, and captured
run. Fixed Codex evidence covers bounded WebSocket rejection and Responses HTTP
fallback; it does not prove successful Responses WebSocket semantics or TUI
interaction. The report also does not prove physical sleep or network removal,
power-loss durability, arbitrary Agent or provider compatibility, reverse
protocol translation, exact JA3/JA4 or HTTP/2 fingerprint parity, Root
installation, signed/notarized distribution, Server, Windows/Linux, or
Preview/Release readiness.
