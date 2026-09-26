# Authorize Runtime Users through Environments

A Runtime User policy authorizes Capture creation through an explicit set of published Environments, rather than maintaining a second Account ACL. This keeps the server-side grant check aligned with the frozen policy that already owns Routes and Account selection: permission to launch an Environment permits only the Accounts that Environment can resolve, while Account history, credentials, other users' evidence, and owner controls remain separately authorized.

Usage thresholds are soft warnings over retained, protocol-declared ViberMate observations. They do not block traffic and are never presented as provider quota, billing, estimated cost, or a hard budget; those would require independent pricing provenance and concurrent reservation semantics.
