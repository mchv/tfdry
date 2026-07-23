# syntax=docker/dockerfile:1.6
# Reproducible end-to-end benchmark: tfdry vs terraform fmt/validate.
# Usage:
#   docker build -f bench/Dockerfile -t tfdry-bench .
#   docker run --rm -v "$(pwd)/bench/results:/out" tfdry-bench

# ── Stage 1: build tfdry ──────────────────────────────────────────────────────
FROM golang:1.26.3-alpine AS builder

# Optional build-arg: git SHA (or any version string) to embed in the binary.
# Defaults to "dev" so plain `docker build` still works; the bench-e2e Make
# target passes the actual git describe output for traceability.
ARG TFDRY_VERSION=dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/mchv/tfdry/output.Version=${TFDRY_VERSION}" \
    -o /out/tfdry .

# ── Stage 2: bench runtime (Debian slim — has glibc for hyperfine on arm64) ──
FROM debian:bookworm-slim

# Pinned versions. Bump deliberately when refreshing baselines.
ARG TERRAFORM_VERSION=1.9.8
ARG HYPERFINE_VERSION=1.18.0

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    unzip \
    jq \
    time \
    && rm -rf /var/lib/apt/lists/*

# Install terraform (pinned, SHA256-verified).
# Hashes from https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_SHA256SUMS
RUN ARCH=$(dpkg --print-architecture) && \
    case "$ARCH" in \
        amd64) TF_SHA256="186e0145f5e5f2eb97cbd785bc78f21bae4ef15119349f6ad4fa535b83b10df8" ;; \
        arm64) TF_SHA256="f85868798834558239f6148834884008f2722548f84034c9b0f62934b2d73ebb" ;; \
        *) echo "unsupported arch for terraform: $ARCH" >&2; exit 1 ;; \
    esac && \
    curl -fsSL "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_${ARCH}.zip" -o /tmp/tf.zip && \
    echo "${TF_SHA256}  /tmp/tf.zip" | sha256sum -c - && \
    unzip /tmp/tf.zip -d /usr/local/bin && \
    rm /tmp/tf.zip && \
    terraform version

# Install hyperfine via Debian package (pinned, SHA256-verified).
# Hashes computed from official release artefacts.
RUN ARCH=$(dpkg --print-architecture) && \
    case "$ARCH" in \
        amd64) HF_SHA256="29efc2e0e637e88a37d7565872c9411b1b6b797d762b4ef74f9d8d1b572aed49" ;; \
        arm64) HF_SHA256="940c243df265bf7be0a392f9450793f3e50b678669d28bbd64e3ea7584b9b27f" ;; \
        *) echo "unsupported arch for hyperfine: $ARCH" >&2; exit 1 ;; \
    esac && \
    curl -fsSL "https://github.com/sharkdp/hyperfine/releases/download/v${HYPERFINE_VERSION}/hyperfine_${HYPERFINE_VERSION}_${ARCH}.deb" -o /tmp/hf.deb && \
    echo "${HF_SHA256}  /tmp/hf.deb" | sha256sum -c - && \
    dpkg -i /tmp/hf.deb && \
    rm /tmp/hf.deb && \
    hyperfine --version

# Copy tfdry binary from builder.
COPY --from=builder /out/tfdry /usr/local/bin/tfdry
RUN tfdry version

# Copy testdata + scripts.
COPY bench/testdata /testdata
COPY bench/gen-testdata.sh /usr/local/bin/gen-testdata.sh
COPY bench/memory.sh /usr/local/bin/memory.sh
COPY bench/run.sh /usr/local/bin/run.sh
RUN chmod +x /usr/local/bin/gen-testdata.sh /usr/local/bin/memory.sh /usr/local/bin/run.sh

# Generate medium and large testdata at build time.
RUN /usr/local/bin/gen-testdata.sh /testdata/medium 20
RUN /usr/local/bin/gen-testdata.sh /testdata/large 100

# Generate dirty variants of every size for write-mode fmt benchmarks.
# Same content as /testdata/<size> but with leading whitespace stripped, so
# `hclwrite.Format` (and `terraform fmt`) have real work to do. The bench
# script (run.sh) refreshes a working copy from these via hyperfine --prepare
# so each iteration sees fresh dirty input.
RUN mkdir -p /testdata/dirty/small /testdata/dirty/medium /testdata/dirty/large && \
    for size in small medium large; do \
        for f in /testdata/$size/*.tf; do \
            sed -e 's/^[[:space:]]*//' "$f" > /testdata/dirty/$size/$(basename "$f"); \
        done; \
    done

# Pre-run terraform init in every testdata dir so terraform validate doesn't
# need network access during benchmarking. The `|| exit 1` is required
# because Docker invokes RUN via `sh -c` without `set -e`, so a failed
# `terraform init` in the middle of the loop would otherwise be masked by
# a subsequent successful iteration (the loop's exit code is the last
# iteration's). Without this, the image could build with one or more
# partially-initialised testdata dirs and benchmarks would silently lie.
RUN for d in /testdata/small /testdata/medium /testdata/large; do \
        cd "$d" && terraform init -input=false -backend=false || exit 1; \
    done

# Run as non-root for defense in depth. /out is a bind-mounted volume — the
# Makefile target passes --user $(id -u):$(id -g) at runtime so files written
# to bench/results are owned by the host user. Without that flag, container
# UID 1000 must be able to write to the mounted dir.
RUN useradd --create-home --uid 1000 --shell /bin/bash bench && \
    chown -R bench:bench /testdata
USER bench

WORKDIR /
CMD ["/usr/local/bin/run.sh"]
