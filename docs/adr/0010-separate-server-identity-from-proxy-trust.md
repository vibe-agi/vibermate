# Separate Server HTTPS Identity from Proxy CA trust

ViberMate has two different TLS peers: the Runtime Server reached by a browser or launcher, and the authorized AI destination intercepted inside CONNECT. Model their configuration, status and trust separately, even when an existing installation uses the same issuer for both. A team's externally issued server certificate must remain valid independently of the Runtime's Proxy CA, and a browser-only user must not need to install an AI interception CA.

## Consequences

- One setup experience does not imply one universal trust anchor. A certificate for `proxy.example.com` on the outer connection and a Proxy-CA-issued certificate for `api.openai.com` inside CONNECT are correct and expected.
- Preserve existing certificates, pins and volumes during adoption. Do not silently switch HTTPS to HTTP, rotate the Proxy CA, or replace a pinned server identity.
- Certificate renewal, private-server trust bootstrap and Proxy CA replacement need separate acceptance tests. The integration branch must not claim that downloading the Proxy CA fixes external server certificate errors.
- Public-CA server identities should use chain, hostname and expiry verification. Existing exact-leaf pins require an explicit migration path; they must not be silently discarded or weakened to make renewal work.
