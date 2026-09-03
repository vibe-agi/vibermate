# Make Environment authority explicit per Capture

Status: accepted

Each managed Capture selects exactly one Environment when `vibermate run` starts. Each Client Flow inside that Environment has a closed Destination Plan: preserve the client-owned Original Destination, or use an explicit Upstream Route; an Original Destination has no synthetic Route, Account, or Model Mapping. We reject workspace defaults, Environment inheritance from a resumed Client Session, and Environment switching inside a running Capture because those conveniences hide the authority that actually handled an Exchange and make evidence ambiguous.

The same client-native Session may resume in a later Capture with a different Environment. The new Capture records that new choice without mutating the client's session file or retroactively changing earlier Exchanges.
