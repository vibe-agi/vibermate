# The build context contains only the current Server, CLI, Web assets and
# license prepared by tool/docker/build-local.sh. It never bootstraps from a
# historical GitHub Release, so pruning old releases cannot break this image.
FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40 AS runtime

ARG VIBERMATE_SOURCE_REVISION=unknown

RUN apk add --no-cache ca-certificates curl

LABEL org.opencontainers.image.title="ViberMate Runtime Server" \
      org.opencontainers.image.version="0.1.13" \
      org.opencontainers.image.source="https://github.com/vibe-agi/vibermate" \
      org.opencontainers.image.revision="${VIBERMATE_SOURCE_REVISION}" \
      org.opencontainers.image.licenses="AGPL-3.0-only"

RUN addgroup -S -g 10001 vibermate \
    && adduser -S -D -H -u 10001 -G vibermate -h /data vibermate \
    && mkdir /data \
    && chown 10001:10001 /data \
    && chmod 0700 /data

COPY dist/docker/ /opt/vibermate/

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
LABEL org.opencontainers.image.version="0.1.13-local" \
      io.vibermate.source="working-tree"
