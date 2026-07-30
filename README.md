# VibeMate

This repository contains the production implementation of VibeMate.

The current code is an M0 runtime and executable Access-plan foundation. It
provides a typed `ProductRuntime` lifecycle, an explicit host contract, a
mandatory offline-egress coordination boundary, a real versioned SQLite store
with operation admission and bounded drain, and a complete Access aggregate
with transactional compare-and-swap writes. A pure compiler validates
ownership, references, and declared capabilities before producing the sole
process-local immutable `AccessPlanSnapshot` and deterministic `PlanHash`.

The current M0 plan contains one enabled Agent endpoint, one owned OpenAI Chat
profile and provider target, one account binding that stores only `SecretRef`
and `AuthDriverRef`, one default route set, Direct egress, a fixed model
mapping, the Anthropic Messages to OpenAI Chat codec identity, an explicit
empty pass-through plugin plan, and dependency revisions. `ClientOrigin` and
the actual provider target are separate network identities, and no secret value
can enter the aggregate or snapshot.

SQLite is the only durable Access authority; active-plan publication occurs
after commit. An indeterminate commit or post-commit publication failure marks
only the affected Access projection unavailable, so new reads and writes fail
closed instead of serving an unmarked stale plan. A normal close/reopen recovery
recompiles the same revision and hash from SQLite. Forced process termination,
operating-system failure, and power-loss recovery are not yet proven. Startup
reports only `initialized`; it does not publish product readiness or discovery.
No protocol wire codec, provider transport, proxy data plane, control server,
Desktop shell, Server host, CLI, or product UI exists yet.

The implementation is not Preview-ready or Release-ready.

## Development

The required toolchain is Go 1.25.12. Run the deterministic repository checks
and test layers with:

```text
make check
make test
make test-race
make vet
make vuln
```

The runtime and package ownership map is in
[`docs/module-map.md`](docs/module-map.md).

## License

VibeMate is licensed under the Apache License 2.0. See
[`LICENSE`](LICENSE).
