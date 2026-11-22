# Grant proven protections to recognized clients

ViberMate tells a captured client apart in three tiers, all computed by the
Runtime before launch. A copy whose bytes match a catalogued release is
verified, and that release's evidence lists the protections proven for it. A
copy that matches no release but satisfies a catalogued publisher's macOS code
signature is recognized; the design repository's ADR-0016 (signed identity
client recognition, not this repository's ADR 0016, which is the licensing
decision) introduced that tier. Anything else is generic.

Under ADR-0016 a recognized client receives the launch recipe and the Root and,
deliberately, none of the protections. The rule, as the code stated it: a
feature was proven for one exact release and a different build has not proven
it, so a recognized client inherits no version-specific behaviour. Claude Code
and Codex update themselves, so on an Apple silicon Mac nearly every run is
recognized, and those runs go without:

- Claude Code's launch instruction that leaves stream recovery to ViberMate, so
  a stream that breaks after ViberMate has answered can come back as a second,
  separately billed request;
- classification of Claude Code's built-in file tools, so in Review every Read,
  Edit or Write inside the workspace asks a person, and in Strict every one is
  rejected;
- the data keepalive on Codex's `/v1/responses` stream, so an API-key or
  custom-base Codex can idle out while a tool decision holds its stream.

The fourth protection, Codex falling back to HTTP when the proxy answers a
Responses WebSocket upgrade with 426, already reaches every client: the
operation catalog decides it.

On 2026-11-20 the owner decided, after being told that it reverses this written
rule, to grant these protections to newer builds when each is proven by
capability or by minimum version and re-proven daily.

## Decision

This ADR replaces that one rule of ADR-0016. A recognized client receives the
protections that a compiled-in recognized-client policy grants its client. Each
is proven by a capability rather than a version, applied only where it is used,
and re-proven every day against the newest release.

Everything else in ADR-0016 stands:

- the recognized tier itself;
- the Developer ID requirement generated from identifier and team;
- signature checks only where the platform performs them;
- signer evidence that names a publisher, never a build.

### Who the client is

Only what the Runtime computed decides. Locally, the Runtime hashed the binary
and ran the platform signature check itself. For a remote companion, the Server
validated the claim against its own catalog and accepted it from an
authenticated companion.

A recognized run already stores its tier, its executable label and its catalog
revision. The Runtime looks up the pair (revision, label) in an index it builds
from its own catalog at start-up:

- **The label names exactly one client.** Recognition evaluates only the signers
  catalogued for that label, and a catalog in which two clients share a label is
  refused.
- **A run recorded under another catalog revision names no client** and
  receives nothing.

Protections that change what a client may do without a question, or how it
behaves, go only to a client whose every catalogued signer signs the launched
file itself. Those are the workspace tools and the streaming fallback. Codex's
npm wrapper shape signs a native child rather than the script that is launched,
so Codex receives only the content-free keepalive.

Nothing else changes for the other tiers:

- Recognized confidence stays "configured", and signer evidence gains no field.
- Release evidence and its features are unchanged.
- Generic clients and manual captures receive nothing new.

### What each protection needs where it is used

#### Claude stream fallback

`vibermate run` sets `CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=1`, replacing any
inherited value, for a recognized Claude Code. It does so only when the client's
own target is an authority whose every route is Upstream, which the launch grant
already lists. There the Exchange holds the stream. On Original Destination,
ViberMate performs no recovery, so the client keeps its own.

A verified release keeps its release's policy.

#### Claude workspace tools

In Review and Strict, a tool group from a recognized Claude Code runs without a
question only if all of these hold for every call in it:

- the call names Read, Edit, Write or NotebookEdit;
- the request defines that tool with exactly the shape a canary captured:
  closed property names and types, the required fields, and no additional
  properties;
- every argument the call uses is one the classifier knows and the definition
  declares;
- every path is absolute and resolves inside the local launcher's workspace;
- no Write, Edit or NotebookEdit touches a path that a local tool runs or loads
  as configuration: Git and Husky hooks and Git config, editor tasks, direnv,
  dev containers, and Claude Code's own hooks and MCP servers.

A definition that grows a property makes that tool ask until a ViberMate release
reviews the property, and the canary names it the day it ships. Glob and Grep
join only once a canary has captured them on the wire, and then only with an
explicit absolute `path`.

A verified release is classified as before.

#### Codex data keepalive

A recognized Codex receives ViberMate's fixed data keepalive frame on
`/v1/responses`, chosen per request from the frozen client contract, as a
verified release with the feature already does. Every Codex already receives
this frame on the ChatGPT path.

#### Codex WebSocket fallback

Unchanged: the operation catalog answers every client. Its client half is now
re-proven daily against the production 426.

### What never decides

None of the following ever grants a protection:

- a request header: the Codex `Version` header, `User-Agent`, `x-app`,
  `anthropic-beta` or a session identifier;
- a version the client reports or prints;
- help output;
- a tool name on its own.

Request content can only withdraw a protection from one use. Nothing in this
decision executes the client, and a repository rule keeps process execution out
of the packages that identify clients and decide what they may do.

### Why no minimum version is checked

A recognized build has no trustworthy version:

- its signer evidence names a publisher, not a build;
- the version headers are written by the client;
- asking the binary would mean running a user-writable file inside the Runtime,
  for a number the build itself chose.

A floor would also be open-ended upwards: it would grant the next release before
anyone has seen it.

So each protection is chosen so that a build lacking the capability behaves
exactly as it would without the protection, and never worse. The version bounds
live in the tests instead. The pull-request jobs prove each protection at the
oldest release they install (Claude Code 2.1.220, Codex 0.159.2), and the daily
canary proves it at the newest.

### Proving each release

Each protection has an installed-client test with a negative control. The
policy table names that test beside the grant it proves. A test fails if a row
names a test that does not exist, that the daily canary does not select, or that
only one platform compiles.

The daily canary runs these tests against the newest Claude Code and Codex
releases on Linux and on Apple silicon macOS. On macOS it also proves that those
releases are still recognized by their signature. A checker fails the run when
a cited proof was skipped or did not run.

Failures are reported in two kinds:

- **A failing protected arm** opens an issue that names the protection.
- **A control that stops discriminating** means the client may no longer need
  the protection. It is reported separately.

### Compatibility

| Peer | Behaviour |
| --- | --- |
| New `vibermate`, older App or Server | The CLI decides the fallback from the grant it already receives; the data plane applies its older rules |
| Older `vibermate`, new App or Server | No fallback instruction for a recognized Claude; workspace tools and keepalive apply |
| Desktop restarted under a running command | Recovery recomputes an identical child environment; the fresh Capture's connections get the restarted Runtime's rules |
| Companion and Server on different versions | Unaffected: same catalog revision, no new claim field |
| Downgrade | Nothing was stored |

The control database, the launch grant, the create request, companion
attestations, and the catalog's content and revision are all unchanged. The App
reads the protections from one new read-only route, and every existing response
is unchanged.

### Visibility

`vibermate run` prints one line when it leaves stream recovery to ViberMate, and
says so when the operator switch withholds it. It never claims a protection the
Runtime applies.

The App shows, for each Capture, the protections it receives, how each is
proven, and why one is off or not used on the current routing set. The Runtime
that applies the protections computes this.

### Withdrawal

A failing canary opens an issue and changes nothing at runtime. There are three
ways to withdraw a protection:

- **The operator switch.** Setting `VIBERMATE_RECOGNIZED_CLIENT_PROTECTIONS=off`
  on the Runtime withdraws every recognized-basis protection from new
  connections. Setting it in `vibermate run`'s environment withdraws the
  fallback from new launches.
- **Remove the row.** Removing a protection's row in a release withdraws it from
  every recognized build of that client.
- **Catalogue the build.** One build can be withdrawn precisely by cataloguing
  its exact digest as a release with no features, which outranks signature
  recognition. This costs a catalog revision that the companion and the Server
  must share.

None of these changes stored data or a wire.

## Out of scope

- Minimum-version grants, or reading a client's version in any form.
- Signer entries for Intel Macs. Linux has no signature tier.
- Glob and Grep on the recognized basis before a canary captures them.
- Applying the Upstream-target condition, the closed shape check or the
  execution-bearing path exclusion to exact releases. A verified 2.1.220 keeps
  disabling its own fallback on Original Destination routes, and keeps the
  containment-only classifier.
- Recording each automatic tool approval as Activity.
- A Settings switch for the operator brake, which would need stored
  configuration.

## Delivery

1. This decision, with the policy table empty: the index, the operator switch,
   the basis API and the repository rule, without behaviour change.
2. The Codex proofs with their controls, then the Codex row.
3. The recognition proofs on macOS.
4. The Claude fallback proof, then its row.
5. The workspace-tools proof, the closed-shape gate and the path exclusion, then
   that row.
6. The proof checker and the canary's macOS jobs.
7. The CLI line, the App's protections view, and the documents.
