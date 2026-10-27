package mcpcatalog

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// The digest of a cloud read (CallResult.Digest): the line the chat shows
// under the call once it is back. It counts and lists what the provider
// returned — "3 security groups", "2 servers · Ready, Stopped" — and never
// says what it means about the account: the model does that, and a digest
// that knew how a service behaves would be a conclusion nobody reviewed.
// Times travel as "@" + RFC 3339 for the browser to show in the operator's
// zone, as copilot.DigestTime writes them. Each provider adds the shapes it
// reads most (aws_digest.go, azure_digest.go); the rest goes through
// digestJSON.

const digestMaxNames = 3

// digestJSON is the digest of an answer KubeBolt has no shape of its own
// for: a count of each list, with the items' states; for a single object, its
// state. The Azure sidecar's envelope ({status, message, results}) is read
// through to its results.
func digestJSON(text string) string {
	var v map[string]any
	if json.Unmarshal([]byte(text), &v) != nil {
		return ""
	}
	if inner, ok := v["results"].(map[string]any); ok {
		v = inner
	}
	if d := digestLists(v); d != "" {
		return d
	}
	return digestObjectState(v)
}

// digestLists counts each top-level list of an answer: "4 security groups".
// An answer without lists has no digest here.
func digestLists(v map[string]any) string {
	var keys []string
	for k, val := range v {
		if _, ok := val.([]any); ok && k != "ResponseMetadata" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []string
	for i, k := range keys {
		if i == 2 {
			break
		}
		n := len(v[k].([]any))
		part := fmt.Sprintf("%d %s", n, digestWords(k, n))
		if s := digestStates(digestList(v[k])); s != "" {
			part += " · " + s
		}
		out = append(out, part)
	}
	return strings.Join(out, " · ")
}

// digestStateKeys are the fields an item's state is read from, across the
// providers' spellings, most specific first.
var digestStateKeys = []string{"availabilityState", "provisioningState", "powerState", "State", "state", "Status", "status"}

// digestItemState is an item's state: a string, or an object's name.
func digestItemState(m map[string]any) string {
	for _, k := range digestStateKeys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case map[string]any:
			if s := digestFirstString(v, "Name", "name", "availabilityState", "code"); s != "" {
				return s
			}
		}
	}
	return ""
}

// digestObjectState is a single object's state, from it or from the object
// one level down that carries it: properties, status, or the only field of
// an answer that wraps one object ({"cluster": {...}}).
func digestObjectState(v map[string]any) string {
	if s := digestItemState(v); s != "" {
		return s
	}
	keys := []string{"properties", "status", "results"}
	if len(v) == 1 {
		for k := range v {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		if inner, ok := v[k].(map[string]any); ok {
			if s := digestItemState(inner); s != "" {
				return s
			}
		}
	}
	return ""
}

// digestStates lists the items' states, most frequent first:
// "running ×2, stopped".
func digestStates(items []map[string]any) string {
	counts := map[string]int{}
	var order []string
	for _, it := range items {
		s := digestItemState(it)
		if s == "" {
			continue
		}
		if counts[s] == 0 {
			order = append(order, s)
		}
		counts[s]++
	}
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	var out []string
	for _, s := range order {
		if counts[s] > 1 {
			out = append(out, fmt.Sprintf("%s ×%d", s, counts[s]))
		} else {
			out = append(out, s)
		}
	}
	return strings.Join(out, ", ")
}

// digestEvent is one change in a provider's change log, for
// digestRarestFirst.
type digestEvent struct {
	name, time string
}

// digestRarestFirst lists a change log's names rarest first — the one
// DeleteRoute among forty repeated writes is the line worth reading — with
// the time of a name seen once.
func digestRarestFirst(events []digestEvent) string {
	type name struct {
		name, latest string
		n            int
	}
	byName := map[string]*name{}
	var order []*name
	for _, e := range events {
		nm := byName[e.name]
		if nm == nil {
			nm = &name{name: e.name}
			byName[e.name] = nm
			order = append(order, nm)
		}
		nm.n++
		if e.time > nm.latest {
			nm.latest = e.time
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].n != order[j].n {
			return order[i].n < order[j].n
		}
		return order[i].latest > order[j].latest
	})
	var names []string
	for i, nm := range order {
		if i == digestMaxNames {
			names = append(names, fmt.Sprintf("+%d more", len(order)-digestMaxNames))
			break
		}
		switch {
		case nm.n > 1:
			names = append(names, fmt.Sprintf("%s ×%d", nm.name, nm.n))
		case digestTime(nm.latest) != "":
			names = append(names, nm.name+" "+digestTime(nm.latest))
		default:
			names = append(names, nm.name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "rarest first: " + strings.Join(names, ", ")
}

// digestCamelWords splits a PascalCase or camelCase key into words, an
// acronym kept whole: DBInstances → DB, Instances; logEntries → log, Entries.
func digestCamelWords(key string) []string {
	r := []rune(key)
	var out []string
	start := 0
	for i := 1; i < len(r); i++ {
		upper := unicode.IsUpper(r[i])
		prevUpper := unicode.IsUpper(r[i-1])
		nextLower := i+1 < len(r) && unicode.IsLower(r[i+1])
		// A word starts at an upper after a lower ("natGateways"), or at the
		// last upper of an acronym that a lower follows ("DBInstances").
		if upper && (!prevUpper || nextLower) {
			out = append(out, string(r[start:i]))
			start = i
		}
	}
	return append(out, string(r[start:]))
}

// digestAcronyms keep their case in a digest: "NAT gateways", not "nat gateways".
var digestAcronyms = map[string]string{
	"nat": "NAT", "vpc": "VPC", "vpcs": "VPCs", "db": "DB", "ip": "IP", "acl": "ACL", "acls": "ACLs",
	"kms": "KMS", "sqs": "SQS", "sns": "SNS", "iam": "IAM", "ecr": "ECR", "eks": "EKS", "ebs": "EBS",
	"aks": "AKS", "sql": "SQL", "vm": "VM", "vms": "VMs", "gke": "GKE",
}

// digestWords turns a list's key into words, singular for one:
// SecurityGroups → "security group(s)", NatGateways → "NAT gateway(s)".
func digestWords(key string, n int) string {
	parts := digestCamelWords(key)
	if n == 1 {
		last := parts[len(parts)-1]
		switch {
		case strings.EqualFold(last, "series"):
			// The same in the singular.
		case strings.HasSuffix(last, "ies"):
			parts[len(parts)-1] = strings.TrimSuffix(last, "ies") + "y"
		case strings.HasSuffix(last, "sses"):
			parts[len(parts)-1] = strings.TrimSuffix(last, "es")
		case strings.HasSuffix(last, "s") && !strings.HasSuffix(last, "ss"):
			parts[len(parts)-1] = strings.TrimSuffix(last, "s")
		}
	}
	for i, p := range parts {
		l := strings.ToLower(p)
		if a, ok := digestAcronyms[l]; ok {
			parts[i] = a
		} else {
			parts[i] = l
		}
	}
	return strings.Join(parts, " ")
}

func digestTime(s string) string {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return "@" + t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

func digestPlural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func digestList(v any) []map[string]any {
	raw, _ := v.([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func digestFirstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}
