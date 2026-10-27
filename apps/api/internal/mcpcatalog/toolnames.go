package mcpcatalog

import "sort"

// ToolNames lets the admin view say which tools a connector exposes to each
// consumer before it has ever been bound (mcpgateway.ToolLister). Each answer
// comes from the same allowlist table the binding serves from, so the list
// the admin approves is the list the model gets — minus anything a pinned
// server version no longer has, which the binding reports as missing.

// ToolNames for AWS: the typed call, plus the documentation and region tools
// for Kobi.
func (b *awsBinder) ToolNames(c Consumer) []string {
	specs := awsToolSpecs(c, "")
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}

// ToolNames for Azure: one router per namespace with at least one command the
// consumer may run, plus the direct inventory tools it may call.
func (azureBinder) ToolNames(c Consumer) []string {
	var out []string
	for _, ns := range azureNamespaces {
		for _, cmd := range ns.commands {
			if c == ConsumerKobi || cmd.autopilot {
				out = append(out, ns.name)
				break
			}
		}
	}
	for _, d := range azureDirectTools {
		if c == ConsumerKobi || d.autopilot {
			out = append(out, d.name)
		}
	}
	sort.Strings(out)
	return out
}

// ToolNames for Google Cloud: the <service>_<tool> names the table gives the
// consumer.
func (gcpBinder) ToolNames(c Consumer) []string { return gcpToolNames(c) }

// Reach says how much of the provider a consumer reads, counted in the unit
// the provider's interface is cut in (mcpgateway.ReachLister), so the admin
// page can say it without binding. The tool count alone would say AWS is the
// smallest connector: one typed call reaches every read operation a service
// rule allows.

// Reach for AWS: the services the consumer's rules open.
func (b *awsBinder) Reach(c Consumer) (string, int) { return "AWS services", len(awsRulesFor(c)) }

// Reach for Azure: the (namespace, command) pairs and direct tools the
// consumer may run.
func (azureBinder) Reach(c Consumer) (string, int) {
	n := 0
	for _, ns := range azureNamespaces {
		for _, cmd := range ns.commands {
			if c == ConsumerKobi || cmd.autopilot {
				n++
			}
		}
	}
	for _, d := range azureDirectTools {
		if c == ConsumerKobi || d.autopilot {
			n++
		}
	}
	return "commands", n
}

// Reach for Google Cloud: its tools, one read operation each.
func (gcpBinder) Reach(c Consumer) (string, int) { return "tools", len(gcpToolNames(c)) }
