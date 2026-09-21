# This image packages the published v0.1.10 Linux distribution (Server, CLI,
# and Flutter Web), whose source is 1adc765bd34331e2115339425e45e4b55190f26e.
# The runtime target preserves that release. The local target overlays the
# current Server/CLI/Web built by tool/docker/build-local.sh. See docs/docker.md.
FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40 AS base

RUN apk add --no-cache ca-certificates curl

FROM base AS distribution
ARG TARGETARCH

RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) asset_arch=x86_64; checksum=5225930f264a6032dd0c92f4e118c296bb3f469fb2d56e65785d5f7d985a397f ;; \
      arm64) asset_arch=arm64; checksum=601debadd70ee8881b180d95afdb950eb89e118952e7398d168953ff7cda1b54 ;; \
      *) printf 'Unsupported architecture: %s\n' "${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    archive="ViberMate_0.1.10_linux_${asset_arch}.tar.gz"; \
    curl --fail --location --show-error --retry 3 \
      --output /tmp/distribution.tar.gz \
      "https://github.com/vibe-agi/vibermate/releases/download/v0.1.10/${archive}"; \
    printf '%s  /tmp/distribution.tar.gz\n' "${checksum}" | sha256sum -c -; \
    mkdir /distribution; \
    tar -xzf /tmp/distribution.tar.gz --strip-components=1 -C /distribution; \
    test -x /distribution/vibermated; \
    test -x /distribution/vibermate; \
    test -f /distribution/vibermate-web/index.html; \
    grep -a -q 'vcs.revision=1adc765bd34331e2115339425e45e4b55190f26e' /distribution/vibermated; \
    grep -a -q 'vcs.revision=1adc765bd34331e2115339425e45e4b55190f26e' /distribution/vibermate

FROM base AS runtime

LABEL org.opencontainers.image.title="ViberMate Runtime Server" \
      org.opencontainers.image.version="0.1.10" \
      org.opencontainers.image.source="https://github.com/vibe-agi/vibermate" \
      org.opencontainers.image.revision="1adc765bd34331e2115339425e45e4b55190f26e" \
      org.opencontainers.image.licenses="Apache-2.0"

RUN addgroup -S -g 10001 vibermate \
    && adduser -S -D -H -u 10001 -G vibermate -h /data vibermate \
    && mkdir /data \
    && chown 10001:10001 /data \
    && chmod 0700 /data

COPY --from=distribution /distribution/ /opt/vibermate/

USER 10001:10001
WORKDIR /opt/vibermate
EXPOSE 9666
STOPSIGNAL SIGTERM

# This checks the local HTTPS control listener. It does not send provider
# requests or claim that an upstream model is healthy. The default listener
# uses a private-CA certificate, so only this in-container probe skips PKI.
HEALTHCHECK --interval=30s --timeout=6s --start-period=15s --retries=3 \
  CMD curl --fail --silent --show-error --insecure --max-time 5 \
      https://127.0.0.1:9666/api/v1/server/web-auth >/dev/null || exit 1

ENTRYPOINT ["/opt/vibermate/vibermated"]
CMD ["server", "--listen", "0.0.0.0:9666", "--data-dir", "/data", "--web-root", "/opt/vibermate/vibermate-web", "--transport", "private_ca_tls"]

FROM runtime AS local
ARG VIBERMATE_SOURCE_REVISION=unknown
COPY dist/docker/ /opt/vibermate/
LABEL org.opencontainers.image.version="0.1.10-local" \
      org.opencontainers.image.revision="${VIBERMATE_SOURCE_REVISION}" \
      io.vibermate.source="working-tree"
