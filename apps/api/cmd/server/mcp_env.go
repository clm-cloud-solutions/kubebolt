package main

import (
	"log/slog"
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
)

// The environment that shapes the external MCP connectors. The gateway itself
// is built in main.go (newMCPGateway); what is read and checked from the
// environment lives here.

// mustMCPCatalog builds the ONE connector catalog the process uses — the
// gateway's admin view and tools — or refuses to boot. A catalog that fails
// its own checks would hand the model a tool name the provider rejects on
// every request; refusing to boot is the cheaper failure.
func mustMCPCatalog() *mcpcatalog.Catalog {
	cat, err := mcpcatalog.New()
	if err != nil {
		fatal("mcp connectors: invalid connector catalog", slog.String("error", err.Error()))
	}
	return cat
}

// mcpConnectorsEnabled reads KUBEBOLT_MCP_CONNECTORS: on unless it says off.
func mcpConnectorsEnabled(v string) bool {
	return envFlag(v, true)
}

// mcpAmbientAllowed decides whether the machine's own cloud credentials may
// be offered: always on a single-tenant install, and on a multi-tenant one
// only when KUBEBOLT_MCP_ALLOW_AMBIENT says so.
func mcpAmbientAllowed(multiTenant bool, override string) bool {
	return !multiTenant || envFlag(override, false)
}

// envFlag reads a boolean KUBEBOLT_MCP_* flag in the spellings the agent's
// bool env vars accept (1/0, t/f, true/false, yes/no, y/n, on/off, any case).
// Empty or anything else is def, so a typo never flips a flag.
func envFlag(v string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "t", "true", "yes", "y", "on":
		return true
	case "0", "f", "false", "no", "n", "off":
		return false
	}
	return def
}
