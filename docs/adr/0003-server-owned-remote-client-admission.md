# Keep remote client admission on the Runtime Server

Status: superseded by 0004

`vibermate run --server HOST:PORT` selects one explicit Runtime Server address while `--env` independently selects the initial Environment. A headless Linux `vibermated` and a Mac App expose the same Server contract; Flutter Web manages an already-running Server rather than starting a local daemon.

The Runtime Server owns Client Admission Policy. Its operator may require review or deliberately allow clients on the configured network to create Captures without review. The CLI cannot grant itself that authority, and `run` therefore has no approval-bypass option. Even in no-review mode, remote traffic uses an encrypted Server transport and each Capture receives narrow, short-lived control and proxy capabilities; no provider credential or Server control secret is placed in child argv.
