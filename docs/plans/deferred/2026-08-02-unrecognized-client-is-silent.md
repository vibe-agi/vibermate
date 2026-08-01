# An unrecognized client fails silently

Deferred: 2026-08-02
Found by: `internal/desktophost.TestARealResponsesClientReachesAModelThroughVibermate`
Implementation at the time: `0d8e017`

## What happens

The client catalog carries release evidence — artifact digests — for exactly
one version of each known client. The machine had Codex 0.146.0; the catalog
carries 0.145.0. The launcher correctly declined to treat it as verified, so
it applied the generic recipe and gave the process no trust root.

Codex then failed its TLS handshake against the MITM leaf and printed:

```
ERROR: Reconnecting... 1/5
...
ERROR: stream disconnected before completion: error sending request for url
```

Nothing in vibermate said why. No ConnectionEvent was written, because the
connection never completed a handshake. From the person's side, a client that
worked yesterday stopped working after an update, and the product that broke
it is silent about it.

## Why it was not fixed here

Two fixes are possible and neither belongs in a slice about diagnostics.

Adding 0.146.0 to the catalog means minting artifact digests. Design 06 §4.2
makes the catalog versioned evidence and says a catalog update must not
silently widen what may be decrypted; digests computed from whatever happens
to be installed on a developer's machine are not release evidence, they are a
recording of that machine. That work needs verified release material.

Making the failure visible is a real slice of its own. A launch that could not
be verified is a fact the runtime holds and never surfaces: the CaptureRun
knows its adapter did not match, the launcher knows it injected no root, and
the person sees neither. The window should be able to say "this looks like
Codex 0.146.0, which this build has no evidence for, so it was launched
without a trust root" — before the client fails, not after.

## What the test does meanwhile

It skips with the version it found and the version the catalog carries, so the
gap is stated on every run rather than hidden behind a green suite.
