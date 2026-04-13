# Build stage: runs on native platform, cross-compiles for target
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /kubebolt-api ./cmd/server

FROM alpine:3.21
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates aws-cli
RUN mkdir -p /data
COPY --from=builder /kubebolt-api /usr/local/bin/kubebolt-api
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["kubebolt-api"]
