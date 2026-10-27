package mcpcatalog

import (
	"fmt"
	"regexp"
	"strings"
)

// Tool names reach two model APIs: Anthropic accepts ^[a-zA-Z0-9_-]{1,128}$
// and OpenAI-compatible providers (the fallback, the Free bonus band, the
// Autopilot gateway engine) ^[a-zA-Z0-9_-]{1,64}$. The stricter one binds.
const (
	MaxModelToolName = 64

	// kobiPrefix + id + "__" + local is what Kobi's model sees.
	kobiPrefix = "mcp__"
	// Autopilot reaches cloud tools through KubeBolt's own MCP profile, whose
	// SDK key prefixes every name with "mcp__kubebolt__" (15 chars). The name
	// the profile serves must leave room for it.
	autopilotPrefix        = "cloud_"
	autopilotWirePrefixLen = len("mcp__kubebolt__")
	MaxAutopilotServedName = MaxModelToolName - autopilotWirePrefixLen
)

var (
	connectorIDRE = regexp.MustCompile(`^[a-z][a-z0-9]{1,15}$`)
	localNameRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
)

// ValidateConnectorID rejects ids that would make a model-facing name
// ambiguous. An id may not contain '_' at all: the Kobi form splits on the
// first "__" after "mcp__" and the Autopilot form on the first '_' after
// "cloud_", so an underscore in the id would let two connectors claim the
// same name.
func ValidateConnectorID(id string) error {
	if !connectorIDRE.MatchString(id) {
		return fmt.Errorf("mcpcatalog: invalid connector id %q (want %s)", id, connectorIDRE)
	}
	return nil
}

// ValidateLocalName checks a binding's local tool name and that both
// consumer forms of it fit the model limit.
func ValidateLocalName(connectorID, local string) error {
	if !localNameRE.MatchString(local) || strings.Contains(local, "__") {
		return fmt.Errorf("mcpcatalog: invalid tool name %q on connector %q", local, connectorID)
	}
	if n := len(KobiToolName(connectorID, local)); n > MaxModelToolName {
		return fmt.Errorf("mcpcatalog: Kobi tool name for %s/%s is %d chars (max %d)", connectorID, local, n, MaxModelToolName)
	}
	if n := len(AutopilotToolName(connectorID, local)); n > MaxAutopilotServedName {
		return fmt.Errorf("mcpcatalog: Autopilot tool name for %s/%s is %d chars (max %d)", connectorID, local, n, MaxAutopilotServedName)
	}
	return nil
}

// KobiToolName is the name Kobi's model sees: mcp__<id>__<local>.
func KobiToolName(connectorID, local string) string {
	return kobiPrefix + connectorID + "__" + local
}

// AutopilotToolName is the name KubeBolt's MCP profile serves to Autopilot:
// cloud_<id>_<local>. The Agent SDK / @ai-sdk/mcp then prefix it with
// mcp__kubebolt__.
func AutopilotToolName(connectorID, local string) string {
	return autopilotPrefix + connectorID + "_" + local
}

// IsKobiToolName reports whether a name is in the Kobi external-tool form.
func IsKobiToolName(name string) bool { return strings.HasPrefix(name, kobiPrefix) }

// IsAutopilotToolName reports whether a name is in the Autopilot cloud form.
func IsAutopilotToolName(name string) bool { return strings.HasPrefix(name, autopilotPrefix) }

// ParseToolName splits a model-facing name of the given consumer back into
// (connector id, local name). It is used only to route; whether the tool is
// allowed is decided by comparing against the binding's current tool list,
// never by trusting what parses.
func ParseToolName(consumer Consumer, name string) (connectorID, local string, ok bool) {
	switch consumer {
	case ConsumerKobi:
		rest, found := strings.CutPrefix(name, kobiPrefix)
		if !found {
			return "", "", false
		}
		id, l, found := strings.Cut(rest, "__")
		if !found || id == "" || l == "" {
			return "", "", false
		}
		return id, l, true
	case ConsumerAutopilot:
		rest, found := strings.CutPrefix(name, autopilotPrefix)
		if !found {
			return "", "", false
		}
		id, l, found := strings.Cut(rest, "_")
		if !found || id == "" || l == "" {
			return "", "", false
		}
		return id, l, true
	}
	return "", "", false
}

// ToolName builds the model-facing name for a consumer.
func ToolName(consumer Consumer, connectorID, local string) string {
	if consumer == ConsumerAutopilot {
		return AutopilotToolName(connectorID, local)
	}
	return KobiToolName(connectorID, local)
}
