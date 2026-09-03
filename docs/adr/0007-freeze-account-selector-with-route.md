# Freeze Account Selector authority with one Upstream Route

Status: accepted

An Upstream Route chooses its Account through exactly one Account Selection Policy: either one fixed Account or one published Account Selector revision. A selector runs once for one Turn and may choose only from the exact Account revisions belonging to the Route's frozen Upstream Endpoint. Its result is validated before any credential is acquired; an exception, timeout, empty result, unavailable Account, or out-of-set Account fails the Exchange without another attempt.

Account Selector JavaScript is a separate Code Library kind from message Transform JavaScript. It receives a read-only request, bounded runtime metadata, and non-secret Account metadata. It cannot mutate the request, observe credentials, change the Endpoint, perform I/O, retain state across Turns, or trigger fallback. Message Transform authority remains unchanged and runs only after Account selection.

Publishing another selector revision changes the Code Library head but never a frozen Environment or active Turn. Selecting that revision in an Environment produces a new Environment revision; a running Capture may adopt it only through the existing next-Turn revision transition.

We reject an ordered fallback after JavaScript selection because it would create two independent Account authorities and make the script's decision untrue in retained evidence. We also reject a mutable global selector because Captures could no longer reconstruct the policy that selected their credentials.
