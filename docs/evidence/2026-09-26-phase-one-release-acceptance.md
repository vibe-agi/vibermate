# Phase-one v0.1.14 release acceptance

Date: 2026-09-26. Candidate branch: `phase1/stabilization`.

This evidence uses synthetic credentials and a local fake provider. It does not
contact a model provider, consume a Codex reset credit, or claim compatibility
with an untested client version.

## Browser and Owner API

The release Web build and current `vibermated` binary ran as real processes.
Playwright drove Chrome 153 through Owner setup and login, published and draft
Environment dry runs, the built-in transparent Environment, retained-evidence
search, storage disclosure, Script Library, long-message expansion, and failure
diagnosis. The dry run selected the expected frozen Route and Account without
connecting to the configured upstream.

The complete local probe passed with these observed results:

- 11 seconds idle: 16 bounded API responses, 12,678 response bytes, and zero
  meaningful semantic-tree mutations.
- Warm short records: p50 788 ms and p95 1,294 ms from provider completion to
  visible UI; 80 ms artificial API delay produced p50 1,287 ms and p95 1,302 ms.
- 151 KiB request: 785 ms to visible locally. Structured 185 KiB content: 791 ms
  to visible and 121 ms to expand.
- The 390 px dry-run action stayed inside the viewport. Search excluded message
  body text while matching retained account/model/status metadata.
- A stopped fake provider was displayed as `provider_transport_failed` with a
  `connection_failed` outbound attempt, not as a proxy accusation.

The accepted remote Server path ran the Linux amd64 candidate on the supplied
test host with private-CA TLS. Its cloud security group exposed only SSH, so the
test did not mutate firewall rules: Playwright used an SSH port forward and a
temporary hostname whose URL, Host, Origin, certificate identity, and advertised
proxy address agreed. The browser completed Owner setup, Endpoint/Account and
Environment publication, manual proxy-login creation, Proxy CA fingerprint
delivery, UI login, and the visible synthetic dry run. Initial Web navigation
over that tunnel took 21.6 s; the dry-run result became visible in 488 ms. The
390 px action remained visible. All remote processes, synthetic data, copied
binaries, credentials, and tunnels were removed afterward.

## Released-data upgrade

The public v0.1.13 Linux x86-64 archive was downloaded with its published
`SHA256SUMS-linux` file and verified before execution. That exact binary created
a synthetic Owner, upstream Endpoint, managed Account, and published Environment.
After a clean stop, the v0.1.14 candidate opened the same data directory. The
Owner signed in, the Account and Environment revision 1 were retained, and a new
Endpoint write succeeded at revision 1. The exact v0.1.13 schema-fixture tests
separately retain a Conversation/Exchange, EgressAttempt, and monotonic audit
sequence. The isolated upgrade directory was removed afterward.

## macOS candidate boundary

The pinned Flutter SDK passed analysis and all 630 Flutter tests (16 explicitly
opt-in live tests skipped), followed by the native Xcode test target. The v0.1.14
release-profile App built and passed the local bundle verifier. Launching a
second packaged GUI was correctly refused because another ViberMate Desktop app
was already active; it was not stopped or modified. Direct `flutter_tester`
sidecar runs reproduced the already documented Keychain denial and therefore do
not substitute for installed-App acceptance.

The final release still requires the protected default-branch workflow to run
the exact candidate through Developer ID signing, Apple notarization and
stapling, Gatekeeper, and isolated installed-App launch. The GitHub Release,
Linux archives/checksums, Homebrew cask, and website must all refer to that exact
tag before this acceptance is complete.
