package seriesgate

import "github.com/kubebolt/kubebolt/packages/shared/metriccatalog"

// ClassifyName says what a stored series' name is: core, custom (a catalog
// family) or unknown. The Enterprise edition's metering counts each class with
// it; here it is the same rule, ready for the views that show the split.
//
// The platform's own series are core whatever their name: only the server
// writes them (the API's HTTP, VictoriaMetrics and agent-door families), and
// both ingest doors drop them from anyone else (IsReservedMetricName), so a
// stored one is ours. Every other name is the shared catalog's call, by exact
// name.
func ClassifyName(name string) metriccatalog.Class {
	if IsReservedMetricName(name) || IsServerOwnedMetricName(name) {
		return metriccatalog.Core
	}
	c, _ := metriccatalog.Classify(name)
	return c
}
