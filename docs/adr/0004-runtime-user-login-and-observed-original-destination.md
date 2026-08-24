# Authenticate Runtime Users and keep Original Destination observable

Status: accepted

A Runtime Server operator creates Runtime Users. A remote CLI authenticates with a username and password and receives a revocable Login Session bound to that Runtime User and Client Device. A valid Login Session may create multiple Capture Runs without a separate approval step. Device discovery or possession of a network address never grants that authority, and the CLI has no approval-bypass option. Provider Accounts remain a separate domain and are never used to authenticate Runtime Users.

The Server may listen on explicitly configured HTTP, self-signed TLS, or operator-provided TLS. Clients never downgrade between them implicitly. HTTP is supported for private and development networks but is presented as unencrypted because login credentials, session authority, and captured traffic can be observed in transit.

Preserving an Original Destination is a routing decision, not a capture bypass. ViberMate keeps the client's destination, authentication, and unmapped model unchanged while still proxying, decrypting supported TLS traffic, parsing supported protocols, and recording evidence. Traffic that cannot be intercepted or parsed is reported as degraded observation rather than silently described as captured.

Every remotely created Capture Run freezes the authenticated Runtime User ID, Login Session ID, Machine ID, human device label, and companion-reported Workspace scope. These are attribution facts, not routing inputs. A native Claude or Codex Session is a separate client-owned identity: exact protocol or local-client evidence may join it across multiple Capture Runs, but changing Environment never rewrites or selects that Session.

Operator usage totals are projections over retained evidence. One terminal Agent API Exchange is one Turn; success, failure, and cancellation come from the Activity terminal state. Requested and upstream models are the exact opaque strings retained on the Exchange. Input, cache-write, cache-read, output, and reasoning token totals preserve known-versus-unknown values; unavailable evidence is reported as partial and is never converted to zero. The projection is rebuildable and does not become billing authority.
