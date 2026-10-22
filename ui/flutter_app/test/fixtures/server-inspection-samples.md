# Synthetic production inspection wire samples

`server-host-summary-samples.json` and `server-cursor-samples.json` are unchanged
outputs from the 2026-10-22 Web detail review's temporary synthetic HostServer
probe. The probe used production HostServer/control composition and production
storage-role inspection readers; it did not read user captures or credentials.

The source probe is described in `docs/reviews/2026-10-22-web-detail-server.md` and
`docs/reviews/2026-10-22-web-detail-ui.md`. Its original synthetic outputs were
archived in `/Users/null/Downloads/vibermate-web-detail-review-20261022`.

The cursor sample includes the exact signed cursor and a successful server
continuation response. Flutter checks only bounded opaque syntax and sends that
cursor unchanged. Proof/source/Exchange/visibility verification remains with the
Runtime; these fixtures make no claim that an old proof authorizes a new Runtime.
