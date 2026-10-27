//go:build !ee

package mcpgateway

// autopilotServed: the Community build has no Autopilot, so the admin view
// reports what Kobi reads and nothing for a consumer that never calls.
const autopilotServed = false
