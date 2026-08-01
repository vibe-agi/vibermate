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

Making the failure visible was done: the verifier now distinguishes a program
nobody catalogued from a catalogued client at an uncatalogued version, and the
launcher writes one line to the terminal the person is already watching before
the client starts. The window shows the same fact now too: the capture panel
names every program started through vibermate, whether anything has actually
come through it, and whether this build has release evidence for it.

What remains is the catalog itself. Until it carries evidence for a release,
that release is named in the window as one that cannot connect — which is
honest, and is not the same as working.

## What the test does meanwhile

It skips with the version it found and the version the catalog carries, so the
gap is stated on every run rather than hidden behind a green suite.
