package copilot

import (
	"regexp"
	"strings"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// A workload's own AWS error names the action that failed first, not always
// everything the call needs: catalog-api's «not authorized to perform:
// s3:GetObject on resource: …/featured.json» hid that the bucket is encrypted
// with a customer-managed KMS key, and Kobi proposed s3:GetObject alone, twice,
// with the AWS skill loaded and two calls left (pod-identity-policy-detached,
// 2026-10-22). A sentence in a 9 KB skill lost to the line in front of it.
// So get_pod_logs hands the rest over where the line is: each distinct
// action and resource AWS refused, and what else guards that kind of
// resource, with the read that shows it. And when they were written: a denial
// that had stopped read as current next to the KMS error that replaced it
// (kms-key-disabled, the same day).

// awsDenialRE matches AWS's authorization error: the action, the resource
// ARN when the message names one (a KMS ciphertext has none), and why.
var awsDenialRE = regexp.MustCompile(`is not authorized to perform: ([a-z0-9-]+:[A-Za-z0-9*]+)(?: on (?:resource: "?(arn:aws[a-z-]*:[a-z0-9-]+:[^\s"]*)|the resource associated with this ciphertext))?`)

var awsDenialWhyRE = regexp.MustCompile(`because no [A-Za-z -]+? allows|with an explicit deny in an? [A-Za-z -]+?(?:policy|boundary)`)

// maxAWSDenials keeps the answer small; more distinct pairs are counted.
const maxAWSDenials = 5

// awsResourceGuards is what else guards a resource of each service, beyond
// the caller's own permission for the action. Facts of AWS, checked against
// its documentation; a service without an entry gets none.
var awsResourceGuards = map[string]string{
	"s3":             "An S3 object is also guarded by its bucket's policy (s3 GetBucketPolicy) and by the bucket's default encryption (s3 GetBucketEncryption). With SSE-KMS under a customer-managed key, reading an object also needs kms:Decrypt on that key, and writing one kms:GenerateDataKey, in the role's policy or the key's.",
	"sqs":            "A queue is also guarded by its access policy and its encryption: sqs GetQueueAttributes gives Policy and KmsMasterKeyId. With a customer-managed key, receiving needs kms:Decrypt on it, and sending kms:GenerateDataKey and kms:Decrypt.",
	"sns":            "A topic is also guarded by its access policy and its encryption: sns GetTopicAttributes gives Policy and KmsMasterKeyId. With a customer-managed key, publishing needs kms:GenerateDataKey and kms:Decrypt on it.",
	"secretsmanager": "A secret is also guarded by its resource policy and its encryption key: secretsmanager DescribeSecret gives KmsKeyId. With a customer-managed key, reading the value also needs kms:Decrypt on it.",
	"kms":            "A key's own policy decides too (kms GetKeyPolicy, PolicyName default), and a disabled key or one pending deletion refuses everyone (kms DescribeKey, KeyState).",
	"ecr":            "A repository is also guarded by its repository policy (ecr GetRepositoryPolicy).",
}

const awsDenialsNote = "AWS refused these calls of the workload's own AWS identity. The action in the error is what failed first, not always all the call needs: the resource can refuse too, and alsoGuardedBy says how and which read shows it. Read it before proposing a permission, and propose everything the call needs. " +
	"from and to are the first and last of those lines (UTC): a denial that stopped before the failure asked about is not its cause."

type awsDenial struct {
	action, resource, why string
	lines                 int
	from, to              time.Time
}

// withAWSDenials adds to payload the AWS authorization errors in logs, every
// line the read kept, and reports whether there were any.
func withAWSDenials(payload map[string]any, logs string) bool {
	if !strings.Contains(logs, "is not authorized to perform") {
		return false
	}
	var order []string
	seen := map[string]*awsDenial{}
	for _, line := range strings.Split(logs, "\n") {
		m := awsDenialRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		resource := strings.TrimRight(m[2], ".,;)")
		key := m[1] + " " + resource
		d := seen[key]
		if d == nil {
			// Why, from what follows the match: the message, not the
			// app's text before it.
			after := line[strings.Index(line, m[0]):]
			d = &awsDenial{action: m[1], resource: resource, why: awsDenialWhyRE.FindString(after)}
			seen[key] = d
			order = append(order, key)
		}
		d.lines++
		if ts, _, stamped := cluster.SplitLogTimestamp(line); stamped {
			if d.from.IsZero() {
				d.from = ts
			}
			d.to = ts
		}
	}
	if len(order) == 0 {
		return false
	}
	items := make([]map[string]any, 0, min(len(order), maxAWSDenials))
	for _, key := range order[:min(len(order), maxAWSDenials)] {
		d := seen[key]
		item := map[string]any{"action": d.action, "lines": d.lines}
		if d.resource != "" {
			item["resource"] = d.resource
		}
		if d.why != "" {
			item["why"] = d.why
		}
		// A plain tail has no kubelet stamps, and then no times.
		if !d.from.IsZero() {
			item["from"] = d.from.UTC().Format(time.RFC3339)
			item["to"] = d.to.UTC().Format(time.RFC3339)
		}
		service, _, _ := strings.Cut(d.action, ":")
		if guard, ok := awsResourceGuards[service]; ok {
			item["alsoGuardedBy"] = guard
		}
		items = append(items, item)
	}
	out := map[string]any{"items": items, "note": awsDenialsNote}
	if left := len(order) - len(items); left > 0 {
		out["omitted"] = left
	}
	payload["awsDenials"] = out
	return true
}
