package mcpcatalog

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The digest of an AWS read (digest.go): "1 table · 2 routes: 10.42.0.0/16 →
// local, 0.0.0.0/0 → nat-0a1b". The shapes the lab reads most get their own
// line here; the rest are counted by digestLists.

// awsTrailDigest: the events kept, the successful reads left out, and the
// event names rarest first — the one DeleteRoute among forty Describe calls
// is the line worth reading — with the time of a name seen once.
func awsTrailDigest(ans awsTrailAnswer, reads int) string {
	head := digestPlural(len(ans.Events), "event", "events")
	if ans.MoreEvents {
		head += " (more on the next page)"
	}
	out := []string{head}
	if reads > 0 {
		out = append(out, fmt.Sprintf("%d read-only left out", reads))
	}
	events := make([]digestEvent, 0, len(ans.Events))
	for _, e := range ans.Events {
		events = append(events, digestEvent{name: e.Event, time: e.Time})
	}
	if rarest := digestRarestFirst(events); rarest != "" {
		out = append(out, rarest)
	}
	return strings.Join(out, " · ")
}

// awsDigest is the digest of any other AWS answer: the shapes the lab reads
// most get their own line, the rest a count of each list in the answer.
func awsDigest(service, operation string, value json.RawMessage) string {
	var v map[string]any
	if json.Unmarshal(value, &v) != nil {
		return ""
	}
	switch awsNorm(service) + "." + awsNorm(operation) {
	case "ec2.describeroutetables":
		return awsRouteTablesDigest(v)
	case "ec2.describeinstances":
		var instances []map[string]any
		for _, r := range digestList(v["Reservations"]) {
			instances = append(instances, digestList(r["Instances"])...)
		}
		return awsCountWithStates(len(instances), "instance", "instances", instances)
	case "ec2.describenatgateways":
		gws := digestList(v["NatGateways"])
		return awsCountWithStates(len(gws), "NAT gateway", "NAT gateways", gws)
	}
	return digestLists(v)
}

func awsRouteTablesDigest(v map[string]any) string {
	tables := digestList(v["RouteTables"])
	var routes []string
	for _, t := range tables {
		for _, r := range digestList(t["Routes"]) {
			dest := digestFirstString(r, "DestinationCidrBlock", "DestinationIpv6CidrBlock", "DestinationPrefixListId")
			target := digestFirstString(r, "GatewayId", "NatGatewayId", "TransitGatewayId", "NetworkInterfaceId",
				"VpcPeeringConnectionId", "InstanceId", "EgressOnlyInternetGatewayId", "LocalGatewayId", "CarrierGatewayId")
			if target == "" {
				target = "?"
			}
			route := dest + " → " + target
			if st, _ := r["State"].(string); st != "" && st != "active" {
				route += " (" + st + ")"
			}
			routes = append(routes, route)
		}
	}
	out := digestPlural(len(tables), "table", "tables") + " · " + digestPlural(len(routes), "route", "routes")
	if len(routes) == 0 {
		return out
	}
	shown := routes
	if len(shown) > digestMaxNames {
		shown = append(append([]string(nil), routes[:digestMaxNames]...), fmt.Sprintf("+%d more", len(routes)-digestMaxNames))
	}
	return out + ": " + strings.Join(shown, ", ")
}

// awsCountWithStates: "3 instances · running ×2, stopped".
func awsCountWithStates(n int, one, many string, items []map[string]any) string {
	out := digestPlural(n, one, many)
	if s := digestStates(items); s != "" {
		out += " · " + s
	}
	return out
}
