# Live provider run

Date: 2026-08-02
Implementation: `6a2ad19` plus the egress-identity fix in this commit
Test: `internal/productruntime.TestALiveProviderAnswersThroughTheWholePipeline`

## What ran

A client request in Anthropic Messages went through the whole ProductRuntime
to a real OpenAI-compatible backend and came back in Anthropic Messages.

```
VIBERMATE_LIVE_PROVIDER_ORIGIN=http://127.0.0.1:23333/v1 \
VIBERMATE_LIVE_PROVIDER_KEY=<redacted> \
VIBERMATE_LIVE_PROVIDER_MODEL=dashscope:glm-5 \
go test ./internal/productruntime/ -run TestALiveProvider -count=1 -v
```

The run passed in 1.22s. The model answered `"ready"`.

## What it proves

- the frozen Access plan resolves and the model policy substitutes the backend
  model for the client's alias;
- the credential is stored behind a SecretRef in the development file store,
  resolved at the boundary, and injected as a provider header;
- the request is translated Anthropic Messages → OpenAI chat completions, and
  the answer is translated back, with usage carried through;
- the outbound is recorded as a completed provider attempt against the real
  destination, with a non-zero received byte count;
- the credential does not appear in the audit record.

## What it does not prove

The request was executed through the Exchange executor directly, not through a
real client over the loopback proxy with TLS interception. The MITM path, the
CONNECT authorization, and the client's own transport are covered by other
tests but not by this one.

The backend is a local OpenAI-compatible service over loopback cleartext. A
strict-TLS provider origin exercises a different transport path.

## What it found

Every provider request had been failing. `internal/exchange` passed one
generated identity as both the upstream attempt ID and the outbound's ID, so
the outbound could not be recorded — ADR-0015 §10 refuses an identity that
encodes its parent — and the Exchange failed with `provider_transport_failed`.

No test caught it, because every other test in the suite substitutes a fake
provider that never reaches the audit. This is the first request that actually
went out.
