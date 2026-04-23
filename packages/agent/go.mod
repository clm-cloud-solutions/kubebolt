module github.com/kubebolt/kubebolt/packages/agent

go 1.25.0

require (
	github.com/kubebolt/kubebolt/packages/proto v0.0.0
	google.golang.org/grpc v1.68.0
	google.golang.org/protobuf v1.36.8
)

require (
	golang.org/x/net v0.29.0 // indirect
	golang.org/x/sys v0.25.0 // indirect
	golang.org/x/text v0.18.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240903143218-8af14fe29dc1 // indirect
)

replace github.com/kubebolt/kubebolt/packages/proto => ../proto
