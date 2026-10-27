package mcpcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// kms-key-disabled: a lookup by the key's bare id found its creation and not
// the DisableKey, which names the key by its ARN.
func TestAWSBindingNotesALookupByAKMSKeyID(t *testing.T) {
	rig := newAWSRig(t)
	b := rig.bind(t, awsModeAmbient)
	lookup := func(value string) CallResult {
		t.Helper()
		res, err := b.Call(context.Background(), ConsumerKobi, awsToolCall,
			callArgs("cloudtrail", "LookupEvents", fmt.Sprintf(`{"LookupAttributes":[{"AttributeKey":"ResourceName","AttributeValue":%q}]}`, value), ""))
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", value, err, res)
		}
		return res
	}
	for _, id := range []string{"1234abcd-12ab-34cd-56ef-1234567890ab", "mrk-1234abcd12ab34cd56ef1234567890ab"} {
		if notes := strings.Join(lookup(id).Notes, "\n"); !strings.Contains(notes, awsTrailKeyIDNote) || strings.Contains(notes, id) {
			t.Errorf("%s: notes = %q", id, notes)
		}
	}
	for _, other := range []string{"arn:aws:kms:us-east-2:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab", "sg-0db"} {
		if notes := strings.Join(lookup(other).Notes, "\n"); strings.Contains(notes, awsTrailKeyIDNote) {
			t.Errorf("%s got the key id note", other)
		}
	}
}

// kms-key-disabled: the lab's boundary denies everything outside us-east-2,
// and a simulation without aws:RequestedRegion answered explicitDeny for
// reads the role was allowed.
func TestAWSBindingSimulationGetsTheResourcesRegion(t *testing.T) {
	rig := newAWSRig(t)
	var sent, sentRegion string
	rig.fake.with(func(f *fakeAWSMCP) {
		f.runScript = func(svc, op, region, params string) (string, bool) {
			sent, sentRegion = params, region
			return fmt.Sprintf(`{"status":"success","return_value":{"EvaluationResults":[]},"api_calls":[{"service":%q,"operation":%q,"status":"success"}]}`, svc, op), false
		}
	})
	b := rig.bind(t, awsModeAmbient)
	simulate := func(params string) (CallResult, []map[string]any) {
		t.Helper()
		res, err := b.Call(context.Background(), ConsumerKobi, awsToolCall, callArgs("iam", "SimulatePrincipalPolicy", params, ""))
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", params, err, res)
		}
		var req struct{ ContextEntries []map[string]any }
		if err := json.Unmarshal([]byte(sent), &req); err != nil {
			t.Fatalf("params sent: %q", sent)
		}
		return res, req.ContextEntries
	}

	res, entries := simulate(`{"PolicySourceArn":"arn:aws:iam::111122223333:role/tienda/catalog","ActionNames":["s3:GetObject","kms:Decrypt"],"ResourceArns":["arn:aws:s3:::tienda-featured/featured.json","arn:aws:kms:eu-west-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab"]}`)
	if len(entries) != 1 || entries[0]["ContextKeyName"] != "aws:RequestedRegion" || fmt.Sprint(entries[0]["ContextKeyValues"]) != "[eu-west-1]" {
		t.Fatalf("context = %v", entries)
	}
	if notes := strings.Join(res.Notes, "\n"); !strings.Contains(notes, fmt.Sprintf(awsSimulateRegionNote, "eu-west-1")) {
		t.Errorf("notes = %q", notes)
	}

	// No regional ARN: the call's own region.
	_, entries = simulate(`{"PolicySourceArn":"arn:aws:iam::111122223333:role/r","ActionNames":["s3:GetObject"],"ResourceArns":["arn:aws:s3:::b/k"]}`)
	if len(entries) != 1 || fmt.Sprint(entries[0]["ContextKeyValues"]) != "["+sentRegion+"]" {
		t.Fatalf("context = %v, call region %s", entries, sentRegion)
	}

	// A region the model set is left alone, and nothing is said.
	res, entries = simulate(`{"PolicySourceArn":"arn:aws:iam::111122223333:role/r","ActionNames":["s3:GetObject"],"ContextEntries":[{"ContextKeyName":"aws:requestedregion","ContextKeyValues":["us-west-2"],"ContextKeyType":"string"}]}`)
	if len(entries) != 1 || fmt.Sprint(entries[0]["ContextKeyValues"]) != "[us-west-2]" || len(res.Notes) != 0 {
		t.Fatalf("context = %v notes = %q", entries, res.Notes)
	}
}

func TestAWSSharedRegion(t *testing.T) {
	for want, arns := range map[string][]string{
		"us-east-2": {"arn:aws:s3:::b/k", "arn:aws:kms:us-east-2:1:key/x"},
		"":          {"arn:aws:sqs:us-east-2:1:q", "arn:aws:sqs:eu-west-1:1:q"},
	} {
		if got := awsSharedRegion(arns); got != want {
			t.Errorf("%v: %q, want %q", arns, got, want)
		}
	}
	if got := awsSharedRegion([]string{"arn:aws:iam::1:role/r"}); got != "" {
		t.Errorf("an IAM ARN gave %q", got)
	}
}

// sqs-consumer-throttled: the redrive policy was read and the dead-letter
// queue was called empty with some 400 orders in it.
func TestAWSBindingNotesAQueuesDeadLetterQueue(t *testing.T) {
	rig := newAWSRig(t)
	var reply string
	rig.fake.with(func(f *fakeAWSMCP) {
		f.runScript = func(svc, op, region, params string) (string, bool) { return reply, false }
	})
	b := rig.bind(t, awsModeAmbient)
	read := func(attrs string) CallResult {
		t.Helper()
		reply = fmt.Sprintf(`{"status":"success","return_value":{"Attributes":%s},"api_calls":[{"service":"sqs","operation":"GetQueueAttributes","status":"success"}]}`, attrs)
		res, err := b.Call(context.Background(), ConsumerKobi, awsToolCall,
			callArgs("sqs", "GetQueueAttributes", `{"QueueUrl":"https://sqs.us-east-2.amazonaws.com/111122223333/orders","AttributeNames":["All"]}`, ""))
		if err != nil || res.IsError {
			t.Fatalf("%v %+v", err, res)
		}
		return res
	}
	withDLQ := read(`{"ApproximateNumberOfMessages":"458","RedrivePolicy":"{\"deadLetterTargetArn\":\"arn:aws:sqs:us-east-2:111122223333:orders-dlq\",\"maxReceiveCount\":5}"}`)
	notes := strings.Join(withDLQ.Notes, "\n")
	if !strings.Contains(notes, awsDLQNote) || strings.Contains(notes, "orders-dlq") {
		t.Errorf("notes = %q", notes)
	}
	// The dead-letter queue itself, or a queue without one: nothing.
	if res := read(`{"ApproximateNumberOfMessages":"404","RedriveAllowPolicy":"{\"redrivePermission\":\"allowAll\"}"}`); strings.Contains(strings.Join(res.Notes, "\n"), awsDLQNote) {
		t.Errorf("a queue without a redrive policy got the note: %q", res.Notes)
	}
}
