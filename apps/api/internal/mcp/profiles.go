package mcp

// uiOnlyTools are Kobi tools that exist for Kobi's panel and mean nothing to
// an external MCP host: they are left out of the /mcp catalogue.
//
// offer_cluster_switch deliberately carries no propose_ prefix (it mutates
// nothing — it changes which cluster the operator is looking at), so
// GovernedToolDefinitions does not withhold it and it reaches the read-only
// catalogue. It emits a card only Kobi's panel can draw, so it is filtered
// here instead.
var uiOnlyTools = map[string]struct{}{
	"offer_cluster_switch": {},
}
