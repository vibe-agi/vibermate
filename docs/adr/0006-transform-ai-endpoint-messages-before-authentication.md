# Transform AI Endpoint messages before protected authentication

Status: accepted

User-Agent edits are carried separately from the codec Header bag. Only a value
change (including addition or deletion) relative to the request transform input
overrides the default wire-profile presentation; an unchanged Header does not.
The override is frozen, single-valued, printable ASCII, and at most 512 bytes.
An empty override suppresses net/http's default User-Agent. Account-owned Header
Set/Delete still has final precedence, and arbitrary AuthDriver mutations remain
invalid. The originally observed client User-Agent and raw evidence are retained.
The sample runner and Exchange share the same User-Agent override validation,
so a sample cannot pass with an override the live transform boundary rejects.

Logical Body and Context string values permit every valid Unicode scalar,
including literal U+FFFD. The original JavaScript UTF-16 code units are checked
so unpaired surrogates are rejected rather than silently emitting U+FFFD.

Request transforms run after the upstream protocol has encoded an AI message but before an Account applies protected Header values; response transforms run after the Endpoint responds but before the client protocol is decoded or encoded for the client. They may change only Headers and logical Body content, run fail-closed, and share bounded Turn Context for one logical Exchange, including its internal attempts.

The request stage executes exactly once per Turn. An identical internal attempt reuses that result; a different second input fails closed. A streaming response invokes the response stage once for each complete SSE event. In that invocation `response.body` is the event `data`, while read-only `response.streaming` and `response.eventName` describe its wire context. Response Headers are decided by the first complete event and must remain identical for later events. This preserves streaming with at most one complete-event startup delay rather than buffering the full Turn.

Core removes compression and framing before presenting a logical message to JavaScript and recalculates representation validators, Content-Length, and transfer framing afterward. Credential-shaped Headers are invisible to JavaScript; original protected values are restored when client-owned passthrough requires them, and managed Account authentication is applied only after transformation. Script-added credential-shaped Headers are discarded.

Raw HTTP evidence remains the original provider wire. Semantic content evidence represents the transformed message visible to the client. A transform failure before the first downstream commit exposes nothing; after a streaming commit it produces one in-band terminal failure. The client-facing error is fixed, non-echoing, and non-retryable.

We reject Session-persistent script state, arbitrary time, randomness, network, filesystem, or process access, and mutation of destinations, methods, URLs, or response status because those would make routing authority, isolation, and evidence unverifiable. Model discovery, health, control, and Runtime Server traffic are outside this capability; only AI Endpoint messages selected by a traffic path can be transformed.
