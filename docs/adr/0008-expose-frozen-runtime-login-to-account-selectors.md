# Expose the frozen Runtime login username only to Account Selectors

Status: accepted

An Account Selector may read `runtime.login.username`, the canonical ViberMate Runtime User username authenticated by the Runtime Server and frozen with the Capture Run. This is the only exception to ADR 0004's rule that Runtime User attribution is not a routing input. The selector still chooses only from its Route's frozen Account set and remains fail-closed.

`runtime.login.username` comes only from the authenticated Login Session carried through the frozen Capture authority. Request headers, request bodies, prompts, client-reported runtime metadata, and the local operating-system username cannot set or override it. A local Capture without a Runtime login receives an empty username; a policy that requires login-based routing must reject that Turn explicitly rather than fall back.

`runtime.user.name` keeps its existing meaning: the operating-system user observed on the client device, useful for local redaction but not authentication. No password, Session Token, credential, Runtime User ID, Login Session ID, or device authority is exposed to JavaScript.
