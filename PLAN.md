# Environment-first Production Vertical

Status: local production vertical implemented; packaged evidence pending

## Goal

Ship the first honest Desktop vertical around two product authorities:

- an Environment is an immutable revisioned configuration aggregate; and
- a Capture is a running source with one independently switchable Environment
  assignment.

The retired Access/Profile product model is not retained through aliases,
dual writes, compatibility readers, or legacy database migrations.

## Required vertical

1. `system_transparent` is always available and performs blind forwarding with
   body-free connection and egress evidence. It never receives a local Root,
   parses semantic content, invokes plugins, or rewrites credentials.
2. A custom Environment owns exact ClientEndpoints, ProtocolPlans, Routes,
   account references, policy bindings, and egress selection. Draft, impact
   preview, and CAS publication form one atomic authority path.
3. `vibermate run --env <id> -- <agent>` and Manual Capture create a typed
   Capture assignment. The launch boundary freezes the Environment revision,
   digest, protected origins, and managed-credential origins.
4. Every admitted request freezes
   Environment -> ClientEndpoint -> ProtocolPlan -> Route -> ProviderAccount
   references. A later publish or Capture switch cannot rewrite an in-flight
   request.
5. Compatible assignment changes are hot; protocol-sensitive changes drain
   affected connections; authority expansion requires a new Capture launch.
6. SQLite is the durable authority for Environment revisions, Capture
   assignments, ProviderAccount configuration, activities, approvals, and
   launch boundaries. Secret bytes remain exclusively in SecretStore.
7. ProductRuntime, Desktop Control API, CLI, and React consume those same
   typed authorities. No UI projection invents missing values or reconstructs
   historical evidence from current configuration.

## Freeze gates

- all Go tests, race tests, vet, formatting, module integrity, and structural
  repository checks pass;
- the compact React workbench passes strict TypeScript, component tests, and
  desktop/mobile Playwright flows;
- the development App starts the production composition and exits cleanly;
- a clean committed candidate produces current deterministic packaged
  acceptance evidence bound to its exact App and sidecars; and
- current documentation, API names, locales, and generated artifacts contain
  no retired Access/Profile product authority.

## Explicitly deferred

- linked client-session account connectors, live credentialed-provider
  acceptance, and automatic account failover;
- plugin execution and the Language Bridge;
- quality evaluation and long-term usage/cost analytics;
- Server/LAN composition and remote enrollment;
- system trust-store mutation and Keychain;
- application-wide capture through Network Extension/TUN;
- signed/notarized distribution and Preview/Release claims.

Deferral keeps those seams typed; it does not permit placeholder success,
fabricated evidence, or fallback to the retired model.
