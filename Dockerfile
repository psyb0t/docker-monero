# Production Dockerfile - multi-stage build.
#
# Every FROM is pinned by digest, not by tag: a tag is mutable, so an
# upstream republish would otherwise change the bytes a rebuild picks up.
# Bump them deliberately.

# Official Monero CLI release. The SHA-256 of each tarball is pinned from
# hashes.txt as published at https://www.getmonero.org/downloads/hashes.txt,
# clearsigned by binaryFate (fingerprint 81AC 591F E9C4 B65C 5806 AFC3
# F0AF 4D46 2A0B DF92). To upgrade: download the new hashes.txt, verify its
# signature against that key, then copy the two linux lines below.
FROM debian:trixie-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f AS monero

ARG TARGETARCH
ARG MONERO_VERSION=0.18.5.4
ARG MONERO_SHA256_AMD64=ba2928962c101b609434bf8c1b43ad59ebe96c5525977284ca599d8bf0f02d10
ARG MONERO_SHA256_ARM64=d20d71e6522b41eb8e549c3425f7f4622c9c9a448fc7147525393fd0a6b59703

RUN apt-get update && \
    apt-get install -y --no-install-recommends bzip2 ca-certificates curl && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /tmp/monero

# The download is checked against the pinned hash before anything in it is
# extracted. Only the two binaries this image runs are kept.
# TARGETARCH is set by BuildKit; the legacy builder (used by testcontainers)
# leaves it empty, so fall back to the build stage's own architecture.
RUN set -eu; \
    arch="${TARGETARCH:-$(dpkg --print-architecture)}"; \
    case "${arch}" in \
        amd64) platform=x64; sha256="${MONERO_SHA256_AMD64}"; dir=x86_64-linux-gnu ;; \
        arm64) platform=armv8; sha256="${MONERO_SHA256_ARM64}"; dir=aarch64-linux-gnu ;; \
        *) echo "unsupported architecture: ${arch}" >&2; exit 1 ;; \
    esac; \
    tarball="monero-linux-${platform}-v${MONERO_VERSION}.tar.bz2"; \
    curl -fsSL --proto '=https' --tlsv1.2 -o "${tarball}" \
        "https://downloads.getmonero.org/cli/${tarball}"; \
    echo "${sha256}  ${tarball}" | sha256sum -c -; \
    tar -xjf "${tarball}"; \
    install -m 0755 "monero-${dir}-v${MONERO_VERSION}/monerod" /usr/local/bin/monerod; \
    install -m 0755 "monero-${dir}-v${MONERO_VERSION}/monero-wallet-rpc" /usr/local/bin/monero-wallet-rpc; \
    rm -rf /tmp/monero/*

# Go build. CGO is off, so the binary is static and runs on the Debian
# runtime unchanged.
FROM golang:1.26.9-alpine@sha256:cdfd4fe2da6b225d8b40c6b7a105736e548e83ff56d5d8f9394446eeb5eb84e0 AS builder

ARG BUILD_COMMIT=""
ARG BUILD_VERSION="dev"

# Coverage instrumentation for the integration suite, empty for a real build.
# The suite passes "-cover -coverpkg=<module>/..." so the containerised
# service writes covdata to GOCOVERDIR and its lines count toward the gate.
ARG BUILD_COVER=""

WORKDIR /app

# Only what the build reads. Tests, docs, and tooling never enter a layer.
COPY go.mod go.sum ./
COPY vendor/ vendor/
COPY cmd/ cmd/
COPY internal/ internal/
COPY pkg/ pkg/

# main.appName comes from go.mod's module path so the binary introduces
# itself by its own name in --help.
RUN APP_NAME="$(head -n 1 go.mod | awk '{print $2}' | awk -F'/' '{print $NF}')" && \
    CGO_ENABLED=0 go build -mod=vendor ${BUILD_COVER} \
    -ldflags "-X main.appName=${APP_NAME} -X main.buildCommit=${BUILD_COMMIT} -X main.buildVersion=${BUILD_VERSION}" \
    -o ./build/app ./cmd

# Runtime. Monero's release binaries link glibc, so the base is Debian slim
# rather than Alpine.
FROM debian:trixie-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f

# tini reaps the wallet-rpc and monerod children and forwards SIGTERM to the
# service. It runs with -s (subreaper) so it still reaps when an orchestrator
# adds its own init (compose `init: true`, docker run --init) as PID 1.
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates tini && \
    rm -rf /var/lib/apt/lists/*

# A fixed UID/GID keeps a bind-mounted host directory's ownership
# predictable across rebuilds.
RUN groupadd --system --gid 10001 monero && \
    useradd --system --uid 10001 --gid monero --home-dir /data \
        --shell /usr/sbin/nologin monero

# /data holds the blockchain (/data/monerod) and the wallets (/data/wallets).
# Wallet files hold encrypted keys, so the directory is private to the user.
RUN mkdir -p /data/monerod /data/wallets /covdata && \
    chown -R monero:monero /data /covdata && \
    chmod 0700 /data/wallets

COPY --from=monero /usr/local/bin/monerod /usr/local/bin/monero-wallet-rpc /usr/local/bin/
COPY --from=builder --chown=monero:monero /app/build/app /app/docker-monero

LABEL io.modelcontextprotocol.server.name="io.github.psyb0t/monero"

USER monero
WORKDIR /data

# The listeners bind inside the container; Docker decides what is published.
# The runner's shutdown budget covers draining the API, saving every open
# wallet, and monerod flushing its database. `docker stop` needs a timeout
# at least this long (docker stop -t 180, or stop_grace_period in compose).
ENV DOCKER_MONERO_HTTP_LISTEN_ADDRESS=0.0.0.0:8080 \
    DOCKER_MONERO_METRICS_LISTEN_ADDRESS=0.0.0.0:9091 \
    RUNNER_SHUTDOWNTIMEOUT=170s \
    LOG_FORMAT=json

VOLUME ["/data"]

# 18080 P2P, 18089 restricted RPC for wallets such as Cake Wallet, 8080 the
# API and MCP, 9091 Prometheus. Unrestricted RPC (18081) stays on the
# container's loopback and is never exposed.
EXPOSE 18080 18089 8080 9091

HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=120s \
    CMD ["/app/docker-monero", "healthcheck"]

ENTRYPOINT ["/usr/bin/tini", "-s", "--", "/app/docker-monero"]

CMD ["run"]
