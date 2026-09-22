# Child process environment

Open a traffic policy's child-process environment editor. **Do not pass to Agent** lists names observed in the launching CLI and lets you add exact-name blocking rules. **Add / override** configures explicit values. Apply the editor, then save/publish the parent policy. Changes take effect on the next launch; existing processes and system environment are unchanged.

## Snapshot boundary

- `vibermate run` collects names from its own `BaseEnvironment`, before policy filtering. The runtime never substitutes its server process environment for a remote terminal.
- Only variable names are transmitted. No value is collected, shown as a fabricated mask, revealed remotely, or persisted by the snapshot feature. The UI explicitly says values remain in the launching terminal.
- The authenticated capture-grant endpoint validates names, order, uniqueness and size before issuing a grant. A failed or unauthorized grant cannot publish a snapshot. Unknown value-bearing fields are rejected.
- Snapshots are advisory, client-reported evidence. They do not grant authority or change rules. The runtime keeps the newest observation per user/machine in a mutex-protected, bounded in-memory store (16 sources, 256 names and 8 KiB of name bytes per source). Restarting the runtime clears snapshots; policy rules remain durable.
- Management reads are authenticated and on demand, separate from dashboard/activity polling. The existing management read permission is required; a CLI run capability cannot enumerate other launch snapshots.
- Refresh retrieves observations already received by this runtime, not a live read of a terminal. Run through ViberMate again to update one. Missing/partial snapshots do not prevent manual rules or erase selections.

## Policy and safety

Selections compile into the existing `LaunchEnvironmentPolicy.DeleteEnv` exact-name rules. Unchecked names inherit normally. Suggested credential names require an explicit user action; they are a heuristic, not a complete secret detector. Future unmatched names are not blocked automatically.

The existing launch-policy validator remains the authority for set/delete conflicts, limits and runtime-managed variables. Routing, proxy, credential and trust variables are locked in this editor and must be configured in their respective settings. “Runtime-managed” does not mean that every such variable is always removed: the launcher applies the active routing and credential policy.

Filtering inherited variables is **not a sandbox**. It does not restrict access to files, networks or other processes. Blocking `PATH`, `HOME` and other tool settings can break commands or change where configuration is found. Explicit override values are policy settings; use upstream-account management for account credentials.

## Verification

Coverage includes name-only serialization, bounds, source replacement, concurrent reads, rejected capture grants, subprocess filtering, locked names, snapshot failure, selection retention, duplicate rules, and compact layouts. App and Web use the same policy editor and control contract.
