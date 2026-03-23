FROM golang:1.25-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /kubebolt-api ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates aws-cli
COPY --from=builder /kubebolt-api /usr/local/bin/kubebolt-api
EXPOSE 8080
ENTRYPOINT ["kubebolt-api"]
