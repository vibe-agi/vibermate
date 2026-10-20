# Network recovery review

Reviewed from published source `14d487ff` after reports of failures following sleep, network changes and proxy selection changes. The review covers the shared Agent transport, launcher relay, VLESS lifecycle, account discovery and exit-selection boundaries. It does not establish that a particular remote node or network blocks VLESS.

## Confirmed defects and corrections

| Boundary | Reproduced behavior | Correction |
| --- | --- | --- |
| Resolved TCP destinations | A stalled first address consumed the entire caller deadline, preventing a healthy second address from being tried through the selected proxy. | Divide the remaining connection budget among compatible addresses, after validating the complete DNS answer. No proxy or destination fallback is introduced. |
| Proxy connection establishment | The initial TCP dial budget did not bound the complete VLESS/SOCKS negotiation when the caller had no deadline. | Bound resolution and proxy setup to 30 seconds, respecting earlier caller cancellation. Established streams are not owned by this setup deadline. |
| VLESS helper ownership | A successfully started helper that subsequently exited remained cached. Every later operation received the same closed client. | Retire only a confirmed exited and drained helper. Later operations share one new startup using the same immutable credential revision. A failed operation is not replayed and a live helper is not restarted on ordinary dial errors. |
| Local CLI relay | An abandoned HTTP request or disconnected pending CONNECT could retain the raw Runtime connection and its relay slot. | Attach HTTP cancellation to that exact backend connection. Stop and join the cancellation callback before transferring a successful CONNECT to tunnel ownership. |
| Account capability discovery | A network failure while obtaining capabilities became the same nonretryable 422 as a verified incompatible model/mode. | Return retryable 503 for unavailable discovery. A valid catalog that rejects the exact model/mode remains a 422. No model request is sent without verified capability evidence. |

The transport corrections are shared by Agent clients. Capability preference behavior remains scoped to the corresponding upstream adapter. Both OpenAI Responses and Anthropic Messages error envelopes retain their native shapes.

## Selection and recovery boundaries retained

Account selection takes precedence over flow selection, followed by the current global default and the built-in direct exit. New logical operations resolve inherited defaults again. An issued request, a queued authorized operation, or an established tunnel retains its original selection; editing a global default does not replay it through another exit.

An explicit flow exit is part of the Capture's Environment revision. Publishing a different Environment remains distinct from applying it to a running Capture. Local Runtime control and the launcher relay use pinned loopback addresses and do not inherit provider egress.

The execution capability cache stores successful directories only for 45 seconds. Its key includes the resolved egress selection. This review found no permanent negative capability cache. The separate owner-facing catalog can retain a successful display snapshot for its short TTL across a global exit change; this is not the model execution cache.

DoH's transport is an explicit independent setting. A VLESS exit with direct DoH still requires the local network to reach the resolver. Node reachability alone does not verify that full path.

## Verification

Behavioral regressions first reproduced each changed failure:

- `TestConfiguredProxyTriesNextResolvedAddressWithinCallerBudget`
- `TestProxyConnectionEstablishmentHasBudgetWithoutCallerDeadline`
- `TestIncompatibleAnswersDoNotConsumeTheOnlyUsableAddressBudget`
- `TestExitedCoreRecoversOnLaterDialWithFreshBootstrap`
- `TestLocalProxyFrontierClientCancellationReleasesPendingBackend`
- `TestCyberDiscoveryNetworkRecoveryThroughExistingCapture`

Normal and focused race tests cover the shared dialer, VLESS lifecycle, relay cancellation and successful tunnel handoff. A real local SOCKS fixture checks stalled-address recovery. Existing Runtime tests exercise account/flow/global precedence, explicit direct overrides, original credentials, managed refresh and model reads through actual local SOCKS/TLS boundaries. The discovery regression retains one Capture and client across failure and recovery and proves zero model sends before trustworthy discovery.

Go's HTTP client can return a canceled request while deliberately retaining an in-progress CONNECT dial. The relay can react to cancellation or disconnect that reaches its socket; it does not infer cancellation of a still-open client socket or impose a new approval deadline.

These checks establish the corrected code behavior. A physical sleep/network-switch acceptance run and remote VLESS availability remain deployment-specific checks; they are not inferred from local fixture success.
