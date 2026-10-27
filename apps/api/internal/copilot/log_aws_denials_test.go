package copilot

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

const s3DenialLine = `[catalog-api] ERROR GET / -> 502 featured list s3://tienda-featured/featured.json: AccessDenied: User: arn:aws:sts::111122223333:assumed-role/tienda-catalog/eks-tienda-catalog-ap-1 is not authorized to perform: s3:GetObject on resource: "arn:aws:s3:::tienda-featured/featured.json" because no identity-based policy allows the s3:GetObject action (HTTP 403)`

func denialItems(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	b, _ := json.Marshal(payload["awsDenials"])
	var d struct {
		Items   []map[string]any `json:"items"`
		Note    string           `json:"note"`
		Omitted int              `json:"omitted"`
	}
	if err := json.Unmarshal(b, &d); err != nil || d.Note != awsDenialsNote {
		t.Fatalf("awsDenials = %s (%v)", b, err)
	}
	return d.Items
}

// The read of pod-identity-policy-detached: the error names s3:GetObject,
// and the bucket is encrypted with a customer-managed key. The answer must
// say the key is part of the read.
func TestFormatPodLogs_AnAWSDenialSaysWhatElseGuardsTheResource(t *testing.T) {
	start := time.Date(2026, 10, 22, 13, 30, 34, 0, time.UTC)
	var lines []string
	for i := 0; i < 185; i++ {
		lines = append(lines, stampedLine(start.Add(time.Duration(i)*time.Second), s3DenialLine))
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(formatPodLogs(strings.Join(lines, "\n"), "error", nil)), &got); err != nil {
		t.Fatal(err)
	}
	items := denialItems(t, got)
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	it := items[0]
	if it["action"] != "s3:GetObject" || it["resource"] != "arn:aws:s3:::tienda-featured/featured.json" ||
		it["lines"] != float64(185) || it["why"] != "because no identity-based policy allows" {
		t.Errorf("item = %v", it)
	}
	if it["from"] != "2026-10-22T13:30:34Z" || it["to"] != "2026-10-22T13:33:38Z" {
		t.Errorf("from %v to %v", it["from"], it["to"])
	}
	guard, _ := it["alsoGuardedBy"].(string)
	for _, s := range []string{"s3 GetBucketEncryption", "kms:Decrypt", "s3 GetBucketPolicy"} {
		if !strings.Contains(guard, s) {
			t.Errorf("alsoGuardedBy does not name %q: %q", s, guard)
		}
	}
	// Counted from every line, while the logs fold them into one.
	if logs, _ := got["logs"].(string); strings.Count(logs, "\n") != 0 {
		t.Errorf("logs not folded: %d lines", strings.Count(logs, "\n")+1)
	}
}

func TestWithAWSDenials_MessageShapes(t *testing.T) {
	logs := strings.Join([]string{
		"ERROR receive: User: arn:aws:sts::1:assumed-role/r/s is not authorized to perform: sqs:receivemessage on resource: arn:aws:sqs:us-east-2:1:orders with an explicit deny in a permissions boundary: arn:aws:iam::1:policy/b",
		"ERROR decrypt: User: arn:aws:sts::1:assumed-role/r/s is not authorized to perform: kms:Decrypt on the resource associated with this ciphertext because the resource does not exist in this Region",
		"ERROR get: User: arn:aws:sts::1:assumed-role/r/s is not authorized to perform: dynamodb:GetItem on resource: arn:aws:dynamodb:us-east-2:1:table/orders.",
		"INFO all good",
	}, "\n")
	payload := map[string]any{}
	if !withAWSDenials(payload, logs) {
		t.Fatal("no denials found")
	}
	items := denialItems(t, payload)
	if len(items) != 3 {
		t.Fatalf("items = %v", items)
	}
	sqs, kms, ddb := items[0], items[1], items[2]
	if sqs["action"] != "sqs:receivemessage" || sqs["resource"] != "arn:aws:sqs:us-east-2:1:orders" ||
		sqs["why"] != "with an explicit deny in a permissions boundary" || !strings.Contains(sqs["alsoGuardedBy"].(string), "KmsMasterKeyId") {
		t.Errorf("sqs = %v", sqs)
	}
	// Lines without the kubelet's stamp give no times.
	if sqs["from"] != nil || sqs["to"] != nil {
		t.Errorf("times from unstamped lines: %v", sqs)
	}
	if kms["action"] != "kms:Decrypt" || kms["resource"] != nil || !strings.Contains(kms["alsoGuardedBy"].(string), "KeyState") {
		t.Errorf("kms = %v", kms)
	}
	// A service KubeBolt has no facts for gets none; the trailing period is
	// not part of the ARN.
	if ddb["resource"] != "arn:aws:dynamodb:us-east-2:1:table/orders" || ddb["alsoGuardedBy"] != nil {
		t.Errorf("dynamodb = %v", ddb)
	}
}

func TestWithAWSDenials_NoneAndTheCap(t *testing.T) {
	payload := map[string]any{}
	if withAWSDenials(payload, "INFO GET / -> 200\nERROR something else") || payload["awsDenials"] != nil {
		t.Fatalf("denials from lines without one: %v", payload)
	}
	var lines []string
	for i := 0; i < maxAWSDenials+2; i++ {
		lines = append(lines, fmt.Sprintf("User: arn:aws:sts::1:assumed-role/r/s is not authorized to perform: s3:GetObject on resource: \"arn:aws:s3:::b/k%d\"", i))
	}
	payload = map[string]any{}
	withAWSDenials(payload, strings.Join(lines, "\n"))
	if items := denialItems(t, payload); len(items) != maxAWSDenials {
		t.Errorf("%d items, want %d", len(items), maxAWSDenials)
	}
	if left := payload["awsDenials"].(map[string]any)["omitted"]; left != 2 {
		t.Errorf("omitted = %v, want 2", left)
	}
}
