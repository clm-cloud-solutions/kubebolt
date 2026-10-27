package main

import "testing"

// The two MCP flags read the same spellings. ALLOW_AMBIENT used to honour
// only the exact string "true", so "=1" or "=TRUE" was silently ignored while
// the flag next to it accepted both.
func TestMCPEnvFlags(t *testing.T) {
	for _, v := range []string{"1", "t", "true", "TRUE", " yes ", "Y", "on"} {
		if !mcpAmbientAllowed(true, v) {
			t.Errorf("KUBEBOLT_MCP_ALLOW_AMBIENT=%q on multi-tenant: ambient not allowed", v)
		}
		if !mcpConnectorsEnabled(v) {
			t.Errorf("KUBEBOLT_MCP_CONNECTORS=%q: off", v)
		}
	}
	for _, v := range []string{"0", "f", "false", "No", "n", "OFF"} {
		if mcpAmbientAllowed(true, v) {
			t.Errorf("KUBEBOLT_MCP_ALLOW_AMBIENT=%q on multi-tenant: ambient allowed", v)
		}
		if mcpConnectorsEnabled(v) {
			t.Errorf("KUBEBOLT_MCP_CONNECTORS=%q: on", v)
		}
	}
	// Unset or unreadable keeps each flag's default: connectors on, ambient
	// credentials off where other orgs could borrow them.
	for _, v := range []string{"", "maybe"} {
		if mcpAmbientAllowed(true, v) {
			t.Errorf("KUBEBOLT_MCP_ALLOW_AMBIENT=%q on multi-tenant: ambient allowed", v)
		}
		if !mcpConnectorsEnabled(v) {
			t.Errorf("KUBEBOLT_MCP_CONNECTORS=%q: off", v)
		}
	}
	// A single-tenant install's machine is the org's own: the override is
	// not needed, and turning it off does not withdraw it.
	for _, v := range []string{"", "false"} {
		if !mcpAmbientAllowed(false, v) {
			t.Errorf("single-tenant with KUBEBOLT_MCP_ALLOW_AMBIENT=%q: ambient not allowed", v)
		}
	}
}
