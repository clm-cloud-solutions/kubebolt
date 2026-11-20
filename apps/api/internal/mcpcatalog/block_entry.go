package mcpcatalog

import (
	"fmt"
	"regexp"
	"strings"
)

// The platform's blocked list (debt-09) names an operation the way the gateway
// does when it checks a call: the binding's OperationOf, else the tool's local
// name. An entry that matches no such name blocks nothing, while the form
// reads as if it did: in an emergency the operator would believe the
// operation stopped. Each connector's BlockEntry refuses those entries.

// blockEntryMatches reports whether an entry (a name, a prefix ending in `*`,
// or `*`) matches one of names. The rule the settings package applies to a
// call.
func blockEntryMatches(entry string, names []string) bool {
	if entry == "*" {
		return true
	}
	prefix, isPrefix := strings.CutSuffix(entry, "*")
	for _, n := range names {
		if (isPrefix && strings.HasPrefix(n, prefix)) || (!isPrefix && n == entry) {
			return true
		}
	}
	return false
}

// awsToolNames are the AWS binding's local tool names, which name every call
// but the typed one.
var awsToolNames = []string{awsToolCall, awsToolSearchDocs, awsToolReadDocs, awsToolListRegions, awsToolAvailability}

// An AWS operation lowercased, or the start of one before a `*`.
var (
	awsBlockOperationRE = regexp.MustCompile(`^[a-z][a-z0-9]{1,127}$`)
	awsBlockOpPrefixRE  = regexp.MustCompile(`^([a-z][a-z0-9]{0,127})?$`)
)

// awsBlockEntry: the typed call is named `service.operation`, every other call
// by its tool. An entry without a service names neither.
func awsBlockEntry(entry string) string {
	if entry == "*" {
		return ""
	}
	service, op, hasDot := strings.Cut(entry, ".")
	if !hasDot {
		if blockEntryMatches(entry, awsToolNames) {
			return ""
		}
		return fmt.Sprintf("%s names no AWS service: write service.operation (ec2.describeinstances), or service.* to block a whole service", entry)
	}
	if !awsServiceRE.MatchString(service) {
		return fmt.Sprintf("%s: %q is not an AWS service name (ec2, s3, rds…)", entry, service)
	}
	if prefix, ok := strings.CutSuffix(op, "*"); ok {
		if awsBlockOpPrefixRE.MatchString(prefix) {
			return ""
		}
	} else if awsBlockOperationRE.MatchString(op) {
		return ""
	}
	return fmt.Sprintf("%s: %q is not an AWS operation name (describeinstances, getobject…)", entry, op)
}

// azureBlockNames: the commands of the allowlist, and the tools a call
// without a command is named after (the routers and the flat tools).
func azureBlockNames() []string {
	names := make([]string, 0, len(azureIndex())+len(azureNamespaces)+len(azureDirectTools))
	for cmd := range azureIndex() {
		names = append(names, cmd)
	}
	for _, ns := range azureNamespaces {
		names = append(names, ns.name)
	}
	for _, d := range azureDirectTools {
		names = append(names, d.name)
	}
	return names
}

func azureBlockEntry(entry string) string {
	if blockEntryMatches(entry, azureBlockNames()) {
		return ""
	}
	return fmt.Sprintf("%s is not an Azure command KubeBolt runs: use the command's name (storage_account_get), or a prefix ending in *", entry)
}

func gcpBlockEntry(entry string) string {
	names := append(gcpToolNames(ConsumerKobi), gcpToolNames(ConsumerAutopilot)...)
	if blockEntryMatches(entry, names) {
		return ""
	}
	return fmt.Sprintf("%s is not a Google Cloud tool KubeBolt runs: use the tool's name (compute_list_instances), or a prefix ending in *", entry)
}
