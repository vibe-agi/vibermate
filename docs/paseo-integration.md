# Paseo conversation profiles

The open-source plugin is an independent package in
[`integrations/paseo`](../integrations/paseo/README.md). It uses Paseo's public plugin SDK
and the existing managed CLI launcher. It neither imports Core source nor reads a user's
private discovery/login files itself.

The plugin retains a person's explicit `agentId → Environment ID + Server origin` choice
on the Paseo host and supplies that choice again on each interactive session opening.
Each wrapper invokes `vibermate run --env ...`, so Core still freezes one explicit Environment
for each Capture; Core never infers an Environment from a resumed Client Session.
One conversation can produce multiple Captures as its Agent is restarted.

## Launch-choice API

`GET /api/v1/capture-environments` is a read-only, body-free control endpoint.
It accepts the existing CLI control credential on the local App or a saved Runtime User
login-session token on the Server, in `Authorization: Bearer ...`.
No query string, request body, proxy credential, or per-run capability is accepted.

```json
{
  "schema": "vibermate-capture-environments/v1",
  "items": [{ "id": "work", "name": "Work" }]
}
```

Only active, published Environments appear, sorted by ID. Runtime Users see only IDs their
current policy allows; the Server Owner's effective policy continues to allow all.
The local CLI may list local launch choices. Enrolled-client and management principals
are not admitted by this route. Results contain no policy bodies, environment values,
account identities, credentials or conversation content and use `Cache-Control: no-store`.
An empty authorized catalog is `items: []`; unavailable storage is not treated as empty.

`vibermate profiles [--server <host:port|http(s)://host:port>] --json` reads this catalog
using normal discovery or the existing CLI login/trust stores and validates its schema.
It creates no Capture and changes no Environment. Profile enumeration grants no new launch
authority: `vibermate run` rechecks permission, published state, client recognition and
destination compatibility when the selected conversation is actually launched.

## Evidence

Go tests cover authority separation, user filtering, disabled Environments, invalid ingress,
body-free catalog projection, CLI transport and response validation. Plugin tests cover
selection isolation, persistence/restart, failed saves, cancellation/timeout, competing
clients, production resume hooks, wrapper argv/cwd and entry navigation/cleanup.
Both plugin entries compile under the official Paseo `0.11.0-beta.3` plugin compiler.
Physical mobile and live account acceptance are separate from these deterministic checks.
