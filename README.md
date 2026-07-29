# VibeMate

This repository contains the production implementation of VibeMate.

The current code is an M0 runtime and Access snapshot foundation. It provides a
typed `ProductRuntime` lifecycle, an explicit host contract, a mandatory
offline-egress coordination boundary, a real versioned SQLite store with
operation admission and bounded drain, and an Access aggregate with
transactional compare-and-swap writes and process-local immutable snapshots.
SQLite is the only durable Access authority; snapshot publication occurs after
commit. An indeterminate commit or post-commit publication failure marks only
the affected Access projection unavailable, so new reads and writes fail closed
instead of serving an unmarked stale snapshot. A normal close/reopen recovery
from SQLite is tested; forced process termination, operating-system failure,
and power-loss recovery are not yet proven. Startup reports only `initialized`;
it does not publish product readiness or discovery. No control server, compiled
Access plan, proxy data plane, provider integration, Desktop shell, Server
host, CLI, or product UI exists yet.

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
