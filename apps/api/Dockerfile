# Dockerfile for kubebolt-api.
#
# Build context must be the repo root (it pulls in both apps/api and
# packages/proto — apps/api/go.mod has a replace directive into
# ../../packages/proto). From the repo root:
#   docker build -f apps/api/Dockerfile -t kubebolt-api:dev .
#
# Cross-compiles for $TARGETPLATFORM via buildx, so multi-arch builds
# (linux/amd64 + linux/arm64) emit native binaries without QEMU.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

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

# Cross-compile for the target platform.
RUN cd apps/api && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -o /kubebolt-api ./cmd/server

FROM alpine:3.21
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates aws-cli
RUN mkdir -p /data
COPY --from=builder /kubebolt-api /usr/local/bin/kubebolt-api
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["kubebolt-api"]
