package mcpcatalog

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Two reads that answered something misleading, and the note or the context
// KubeBolt now adds to them (kms-key-disabled on the EKS lab, 2026-10-22).

// A CloudTrail lookup by a KMS key's bare id found the key's CreateKey and
// CreateAlias, which name the id, and not the DisableKey that broke it, which
// names the key by its ARN. Kobi then searched by event source and drowned in
// failed Decrypt calls before a ReadOnly=false window found it.
const awsTrailKeyIDNote = "This lookup names a KMS key by its bare id. A key's own changes (DisableKey, EnableKey, ScheduleKeyDeletion, PutKeyPolicy) name it by its ARN, so a lookup by the id can miss them: look them up by the key's ARN, or with ReadOnly=false around when the failure started."

var awsKMSKeyIDRE = regexp.MustCompile(`^(?:mrk-[0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)

// awsTrailByKeyID says whether a LookupEvents request looks a resource up by
// a KMS key's bare id.
func awsTrailByKeyID(params []byte) bool {
	var req struct {
		LookupAttributes []struct {
			AttributeKey   string
			AttributeValue string
		}
	}
	if json.Unmarshal(params, &req) != nil {
		return false
	}
	for _, a := range req.LookupAttributes {
		if a.AttributeKey == "ResourceName" && awsKMSKeyIDRE.MatchString(a.AttributeValue) {
			return true
		}
	}
	return false
}

// A queue's redrive policy was read and its dead-letter queue was not: Kobi
// said it was empty «because without invocations maxReceiveCount is never
// reached», with some 400 orders already in it, and proposed restoring the
// consumer alone (sqs-consumer-throttled, 2026-10-22). A throttled Lambda
// invocation returns the message and counts as a receive.
const awsDLQNote = "This queue has a dead-letter queue (RedrivePolicy, deadLetterTargetArn): a message received maxReceiveCount times without being deleted moves there, and with a Lambda trigger a throttled or failed invocation counts as a receive. Read the dead-letter queue's ApproximateNumberOfMessages before saying it is empty. A fix that restores the consumer leaves those messages there until they are moved back (sqs start-message-move-task)."

// awsQueueHasDLQ says whether a GetQueueAttributes answer names a redrive
// policy. Only the attribute's name is looked for; nothing of the answer
// goes into the note.
func awsQueueHasDLQ(service, operation, text string) bool {
	return awsNorm(service) == "sqs" && awsNorm(operation) == awsNorm("GetQueueAttributes") &&
		strings.Contains(text, `"RedrivePolicy"`)
}

// A simulation without aws:RequestedRegion meets every condition that limits
// regions as a request from none: the lab's permissions boundary denies
// everything outside us-east-2, and SimulatePrincipalPolicy answered
// explicitDeny for s3:GetObject and kms:Decrypt the role was allowed in
// practice. Kobi spent a call on it and had to argue it away. Boundaries and
// SCPs that limit regions are common, so KubeBolt fills the region in when
// the request leaves it out, and says so.
const awsSimulateRegionNote = "KubeBolt added aws:RequestedRegion=%s to ContextEntries: without it, a policy that limits regions (a permissions boundary, an SCP) reads as a deny for every action. Pass ContextEntries with aws:RequestedRegion yourself to simulate another region."

func awsIsSimulate(service, operation string) bool {
	return awsNorm(service) == "iam" && awsNorm(operation) == awsNorm("SimulatePrincipalPolicy")
}

// awsWithRequestedRegion adds aws:RequestedRegion to a simulation's
// ContextEntries when they do not set it: the region the simulated resources
// share, else fallback, the call's region. It returns the params and the
// region it added, or the params unchanged and "".
func awsWithRequestedRegion(params []byte, fallback string) ([]byte, string) {
	var req map[string]json.RawMessage
	if json.Unmarshal(params, &req) != nil || req == nil {
		return params, ""
	}
	var entries []map[string]any
	if raw, ok := req["ContextEntries"]; ok {
		if json.Unmarshal(raw, &entries) != nil {
			return params, ""
		}
		for _, e := range entries {
			if name, _ := e["ContextKeyName"].(string); strings.EqualFold(name, "aws:RequestedRegion") {
				return params, ""
			}
		}
	}
	var arns []string
	if raw, ok := req["ResourceArns"]; ok {
		_ = json.Unmarshal(raw, &arns)
	}
	region := fallback
	if r := awsSharedRegion(arns); r != "" {
		region = r
	}
	if !awsRegionRE.MatchString(region) {
		return params, ""
	}
	entries = append(entries, map[string]any{
		"ContextKeyName":   "aws:RequestedRegion",
		"ContextKeyValues": []string{region},
		"ContextKeyType":   "string",
	})
	raw, err := json.Marshal(entries)
	if err != nil {
		return params, ""
	}
	req["ContextEntries"] = raw
	out, err := json.Marshal(req)
	if err != nil || len(out) > awsMaxParamsBytes {
		return params, ""
	}
	return out, region
}

// awsSharedRegion is the region every regional ARN in arns names, or "" when
// none names one or they differ. S3 and IAM ARNs carry no region.
func awsSharedRegion(arns []string) string {
	region := ""
	for _, a := range arns {
		parts := strings.SplitN(a, ":", 5)
		if len(parts) < 5 || parts[3] == "" {
			continue
		}
		if region != "" && parts[3] != region {
			return ""
		}
		region = parts[3]
	}
	return region
}
