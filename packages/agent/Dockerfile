# Dockerfile for kubebolt-agent.
#
# Build context must be the repo root (it pulls in both packages/agent and
# packages/proto). From the repo root:
#   docker build -f packages/agent/Dockerfile -t kubebolt-agent:dev .

FROM golang:1.25-alpine AS builder

WORKDIR /src

# GOWORK=off tells Go to ignore the repo's go.work (which references modules
# we're not copying into the build context). Module resolution falls back to
# the replace directive in packages/agent/go.mod that points at ../proto.
ENV GOWORK=off
ENV CGO_ENABLED=0
ENV GOOS=linux

# Copy module manifests first so `go mod download` can be cached.
COPY packages/proto/go.mod packages/proto/go.sum ./packages/proto/
COPY packages/agent/go.mod packages/agent/go.sum ./packages/agent/

RUN cd packages/agent && go mod download

# Copy sources.
COPY packages/proto/ ./packages/proto/
COPY packages/agent/ ./packages/agent/

# Build. -s -w strip symbols for a smaller binary. Ends up ~15MB for amd64.
RUN cd packages/agent && \
    go build -ldflags="-s -w" -o /out/kubebolt-agent ./cmd/agent

# Runtime. distroless/static includes CA certs and tzdata, runs as nonroot by
# tag. No shell, no package manager, minimal attack surface.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/kubebolt-agent /kubebolt-agent

USER nonroot:nonroot

ENTRYPOINT ["/kubebolt-agent"]
