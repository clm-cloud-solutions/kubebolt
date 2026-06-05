# Dockerfile for kubebolt-api.
#
# Build context must be the repo root (it pulls in both apps/api and
# packages/proto — apps/api/go.mod has a replace directive into
# ../../packages/proto). From the repo root:
#   docker build -f apps/api/Dockerfile -t kubebolt-api:dev .
#
# Cross-compiles for $TARGETPLATFORM via buildx, so multi-arch builds
# (linux/amd64 + linux/arm64) emit native binaries without QEMU.
# Explicit patch pin (not floating 1.25-alpine) so the build reuses
# the CVE-patched toolchain consistently across CI / release / local.
# Bump in lockstep with .github/workflows/release.yml setup-go.
FROM --platform=$BUILDPLATFORM golang:1.25.11-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

# GOWORK=off so go.mod's replace directive (../../packages/proto) is
# the only thing resolving the proto dependency — go.work isn't needed
# (and we don't copy it into the build context).
ENV GOWORK=off

# Copy module manifests first so `go mod download` is cacheable.
COPY packages/proto/go.mod packages/proto/go.sum ./packages/proto/
COPY apps/api/go.mod apps/api/go.sum ./apps/api/

RUN cd apps/api && go mod download

# Copy sources (only what apps/api needs — proto + api itself).
COPY packages/proto/ ./packages/proto/
COPY apps/api/ ./apps/api/

# Version stamp — release.yml's build-api passes `--build-arg VERSION=<semver>`
# so `main.version` (cmd/server/main.go:47) reports the actual release tag
# instead of staying "dev". A "dev" binary short-circuits the update-check
# package, so without this stamp every self-hosted install never surfaces
# the Topbar "Update vX.Y.Z available" chip — defeats the feature shipped
# in 1.12.1. Default kept at "dev" so local `docker build` from a working
# tree behaves as before. (Forward-ported from main / the 1.13.1 fix.)
ARG VERSION=dev

# Cross-compile for the target platform.
RUN cd apps/api && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w -X main.version=${VERSION}" -o /kubebolt-api ./cmd/server

FROM alpine:3.21
# ca-certificates is required for outbound TLS — the Copilot dialer
# hits Anthropic/OpenAI HTTPS endpoints, and the metrics-storage
# external-URL path may dial managed VM endpoints over TLS. NO
# aws-cli: the api binary has zero AWS code paths (no aws-sdk-go
# import, no exec.Command("aws", ...)) — the package was a leftover
# from the initial Dockerfile template (commit 3ae4516, 2026-03-23)
# that pulled in Python + cryptography + urllib3 + pip as transitive
# deps, carrying ~30MB and ~12 CVEs for zero functional benefit.
# Session 11-A 2026-05-27 found the AR scan flagged all 12 against
# those Python packages exclusively; removal drops them all in one
# stroke. If a future api code path genuinely needs to shell out to
# aws, add the dep back behind a build tag — don't reinstate the
# blanket install.
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates
RUN mkdir -p /data
COPY --from=builder /kubebolt-api /usr/local/bin/kubebolt-api
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["kubebolt-api"]
