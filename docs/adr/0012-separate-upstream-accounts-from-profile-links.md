# Separate upstream accounts from profile links

Upstream credentials now have an independent management surface and lifecycle; service configurations link existing accounts instead of owning their creation and deletion. Explicit links authorize reuse only for the credential's exact origin and a supported authentication driver, so protocol compatibility alone cannot send a credential to a different service.

Association revisions are independent of account identity revisions and credential epochs. Adding a profile must not invalidate already-published routes or duplicate an OAuth refresh owner; unlinking is serialized with policy publication, refuses published references or active leases, and preserves the credential and other links. Account deletion remains a separate guarded operation in the account manager.
