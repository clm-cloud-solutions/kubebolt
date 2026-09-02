module github.com/kubebolt/kubebolt/packages/proto

go 1.25.0

// Forces Go 1.26.6+ — closes the 5 HIGH stdlib CVEs that landed under 1.25.9,
// plus CVE-2026-39821 (x/net/idna), CVE-2026-46600 (x/net/dns/dnsmessage) and
// the 2026-08 stdlib wave, all fixed in 1.26.6. Bumped in lockstep with
// apps/api/go.mod and packages/agent/go.mod.
toolchain go1.26.6

require (
	google.golang.org/grpc v1.83.1
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af
)

require (
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
)
