package mcpcatalog

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// awsTrailEvent builds one LookupEvents entry the way boto3 hands it back:
// the record again, as a JSON string, in CloudTrailEvent.
func awsTrailEvent(t *testing.T, name, readOnly, resource string, record map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	e := map[string]any{
		"EventId":         "id-" + name,
		"EventName":       name,
		"ReadOnly":        readOnly,
		"EventTime":       "2026-10-21T14:03:07.000000+00:00",
		"EventSource":     "ec2.amazonaws.com",
		"Username":        "top-level-name",
		"CloudTrailEvent": string(raw),
	}
	if resource != "" {
		e["Resources"] = []map[string]string{{"ResourceType": "AWS::EC2::SecurityGroup", "ResourceName": resource}}
	}
	return e
}

func awsTrailLookupReply(t *testing.T, next string, events ...map[string]any) json.RawMessage {
	t.Helper()
	if events == nil {
		events = []map[string]any{} // AWS answers "Events": [], never null
	}
	v := map[string]any{"Events": events, "ResponseMetadata": map[string]any{"RequestId": "r"}}
	if next != "" {
		v["NextToken"] = next
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func awsTrailFixture(t *testing.T) json.RawMessage {
	t.Helper()
	revoke := awsTrailEvent(t, "RevokeSecurityGroupIngress", "false", "sg-0db", map[string]any{
		"eventTime":       "2026-10-21T14:03:07Z",
		"eventSource":     "ec2.amazonaws.com",
		"sourceIPAddress": "203.0.113.7",
		"userAgent":       "aws-cli/2.17.0 md/awscrt#0.20.11 ua/2.0 os/macos#24.0.0 md/arch#arm64 lang/python#3.11.9 md/pyimpl#CPython cfg/retry-mode#standard",
		"readOnly":        false,
		"requestParameters": map[string]any{"groupId": "sg-0db", "ipPermissions": map[string]any{"items": []any{
			map[string]any{"ipProtocol": "tcp", "fromPort": 5432, "toPort": 5432, "groups": map[string]any{"items": []any{map[string]any{"groupId": "sg-0nodes"}}}},
		}}},
		"recipientAccountId": "111122223333",
		"userIdentity": map[string]any{
			"type": "AssumedRole", "accountId": "111122223333",
			"arn":            "arn:aws:sts::111122223333:assumed-role/shop-operator/operator-session",
			"sessionContext": map[string]any{"sessionIssuer": map[string]any{"userName": "shop-operator"}},
		},
		"tlsDetails": map[string]any{"tlsVersion": "TLSv1.3", "cipherSuite": "TLS_AES_128_GCM_SHA256"},
	})
	describe := awsTrailEvent(t, "DescribeSecurityGroups", "true", "", map[string]any{
		"eventTime": "2026-10-21T14:02:00Z", "readOnly": true, "sourceIPAddress": "10.0.1.5",
		"userIdentity": map[string]any{"type": "AssumedRole", "arn": "arn:aws:sts::111122223333:assumed-role/aws-load-balancer-controller/i-1"},
	})
	denied := awsTrailEvent(t, "DescribeDBInstances", "true", "", map[string]any{
		"eventTime": "2026-10-21T14:01:00Z", "eventSource": "rds.amazonaws.com", "readOnly": true,
		"errorCode": "AccessDenied", "errorMessage": "User is not authorized to perform rds:DescribeDBInstances",
		"recipientAccountId": "111122223333",
		"userIdentity":       map[string]any{"type": "AssumedRole", "accountId": "444455556666", "arn": "arn:aws:sts::444455556666:assumed-role/KubeBoltBroker/kubebolt-org-1"},
	})
	denied["EventSource"] = "rds.amazonaws.com"
	detach := awsTrailEvent(t, "DetachRolePolicy", "false", "", map[string]any{
		"eventTime": "2026-10-21T13:59:30Z", "eventSource": "iam.amazonaws.com", "readOnly": false,
		"requestParameters": map[string]any{"roleName": "catalog-pod-identity", "policyArn": "arn:aws:iam::111122223333:policy/catalog-s3-read"},
		"userIdentity":      map[string]any{"type": "IAMUser", "userName": "shop-operator", "arn": "arn:aws:iam::111122223333:user/shop-operator"},
	})
	detach["EventSource"] = "iam.amazonaws.com"
	return awsTrailLookupReply(t, "", revoke, describe, denied, detach)
}

func TestAWSCompactTrailKeepsChangesAndFailedReads(t *testing.T) {
	ans, reads, ok := awsCompactTrail(awsTrailFixture(t))
	if !ok {
		t.Fatal("not read as a LookupEvents answer")
	}
	if reads != 1 {
		t.Fatalf("reads left out = %d, want the one Describe that succeeded", reads)
	}
	if len(ans.Events) != 3 {
		t.Fatalf("events = %+v", ans.Events)
	}

	revoke := ans.Events[0]
	if revoke.Event != "RevokeSecurityGroupIngress" || revoke.Time != "2026-10-21T14:03:07Z" || revoke.Source != "ec2" ||
		revoke.Who != "assumed-role/shop-operator/operator-session" || revoke.From != "203.0.113.7" {
		t.Fatalf("revoke row = %+v", revoke)
	}
	if len(revoke.Resources) != 1 || revoke.Resources[0] != "AWS::EC2::SecurityGroup sg-0db" {
		t.Fatalf("resources = %v", revoke.Resources)
	}
	if !strings.Contains(string(revoke.Request), `"fromPort":5432`) || !strings.Contains(string(revoke.Request), "sg-0nodes") {
		t.Fatalf("the revoked rule did not survive: %s", revoke.Request)
	}
	if len(revoke.Agent) > awsTrailAgentMax || !strings.HasPrefix(revoke.Agent, "aws-cli/2.17.0") {
		t.Fatalf("agent = %q", revoke.Agent)
	}

	denied := ans.Events[1]
	if !denied.ReadOnly || !strings.HasPrefix(denied.Error, "AccessDenied: User is not authorized") ||
		denied.Who != "assumed-role/KubeBoltBroker/kubebolt-org-1 (account 444455556666)" || denied.Source != "rds" {
		t.Fatalf("failed read row = %+v", denied)
	}

	detach := ans.Events[2]
	if detach.Who != "user/shop-operator" || detach.Source != "iam" || !strings.Contains(string(detach.Request), "catalog-s3-read") {
		t.Fatalf("detach row = %+v", detach)
	}

	raw, err := json.Marshal(ans)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"CloudTrailEvent", "tlsDetails", "TLS_AES_128_GCM_SHA256", "ResponseMetadata", "aws-load-balancer-controller"} {
		if strings.Contains(string(raw), gone) {
			t.Errorf("%q reached the model", gone)
		}
	}

	notes := strings.Join(awsTrailNotes(ans, reads), "\n")
	for _, want := range []string{awsTrailKeptNote, "left out 1 read-only calls", awsTrailPreviousNote} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
	if strings.Contains(notes, awsTrailEmptyNote) || strings.Contains(notes, awsTrailMoreNote) {
		t.Errorf("notes for an empty or paged answer on a full one:\n%s", notes)
	}
}

func TestAWSCompactTrailIsSmallerThanTheRawAnswer(t *testing.T) {
	raw := awsTrailFixture(t)
	ans, _, ok := awsCompactTrail(raw)
	if !ok {
		t.Fatal("not read")
	}
	out, _ := json.Marshal(ans)
	if len(out)*2 > len(raw) {
		t.Fatalf("compact answer is %d bytes for a %d-byte answer", len(out), len(raw))
	}
}

func TestAWSCompactTrailEmptyAndPagedAnswers(t *testing.T) {
	ans, reads, ok := awsCompactTrail(awsTrailLookupReply(t, ""))
	if !ok || reads != 0 || ans.Events == nil || len(ans.Events) != 0 {
		t.Fatalf("empty answer: %+v %d %v", ans, reads, ok)
	}
	if out, _ := json.Marshal(ans); string(out) != `{"events":[]}` {
		t.Fatalf("empty answer renders as %s", out)
	}
	notes := strings.Join(awsTrailNotes(ans, reads), "\n")
	if !strings.Contains(notes, awsTrailEmptyNote) || strings.Contains(notes, awsTrailPreviousNote) {
		t.Fatalf("notes = %s", notes)
	}

	paged, _, _ := awsCompactTrail(awsTrailLookupReply(t, "tok", awsTrailEvent(t, "AuthorizeSecurityGroupIngress", "false", "sg-1",
		map[string]any{"eventTime": "2026-10-21T10:00:00Z", "userIdentity": map[string]any{"type": "Root", "arn": "arn:aws:iam::111122223333:root"}})))
	if !paged.MoreEvents || paged.Events[0].Who != "root" {
		t.Fatalf("paged = %+v", paged)
	}
	if !strings.Contains(strings.Join(awsTrailNotes(paged, 0), "\n"), awsTrailMoreNote) {
		t.Fatal("no note for the events past this page")
	}
}

func TestAWSCompactTrailOnlyReadsLookupAnswers(t *testing.T) {
	for _, v := range []string{`{"DBInstances":[]}`, `[]`, `"text"`, `{"Events":"x"}`} {
		if _, _, ok := awsCompactTrail(json.RawMessage(v)); ok {
			t.Errorf("%s read as a LookupEvents answer", v)
		}
	}
	if !awsIsTrailLookup("CloudTrail", "lookup_events") || awsIsTrailLookup("cloudtrail", "GetTrailStatus") || awsIsTrailLookup("logs", "LookupEvents") {
		t.Fatal("awsIsTrailLookup")
	}
}

func TestAWSCompactTrailClipsLargeRequestsAndKeepsEventsWithoutARecord(t *testing.T) {
	big := strings.Repeat("x", awsTrailRequestMax*2)
	ans, _, _ := awsCompactTrail(awsTrailLookupReply(t, "",
		awsTrailEvent(t, "PutBucketPolicy", "false", "", map[string]any{
			"eventTime": "2026-10-21T10:00:00Z", "requestParameters": map[string]any{"policy": big},
			"userIdentity": map[string]any{"type": "AWSService", "invokedBy": "cloudformation.amazonaws.com"},
		}),
		map[string]any{"EventName": "ModifyDBParameterGroup", "ReadOnly": "false", "EventTime": "2026-10-21T09:00:00.123000+02:00",
			"EventSource": "rds.amazonaws.com", "Username": "operator", "CloudTrailEvent": "{not json"},
	))
	if len(ans.Events) != 2 {
		t.Fatalf("events = %+v", ans.Events)
	}
	put := ans.Events[0]
	var s string
	if json.Unmarshal(put.Request, &s) != nil || len(s) > awsTrailRequestMax || !strings.HasSuffix(s, "…") {
		t.Fatalf("request kept %d bytes: %.80s", len(put.Request), put.Request)
	}
	if put.Who != "cloudformation.amazonaws.com" || put.Via != "cloudformation.amazonaws.com" {
		t.Fatalf("service caller = %+v", put)
	}
	unparsed := ans.Events[1]
	if unparsed.Who != "operator" || unparsed.Time != "2026-10-21T07:00:00Z" || unparsed.Source != "rds" {
		t.Fatalf("event with an unreadable record = %+v", unparsed)
	}
}

// A rule revoked by its id: the request holds only the id, and the rule itself
// (what to restore) is in AWS's answer. Seen in the lab's sg-rds-ingress-revoked.
func TestAWSCompactTrailKeepsWhatARevokeByIDRemoved(t *testing.T) {
	ans, _, _ := awsCompactTrail(awsTrailLookupReply(t, "",
		awsTrailEvent(t, "RevokeSecurityGroupIngress", "false", "sg-0db", map[string]any{
			"eventTime": "2026-10-21T21:30:04Z", "readOnly": false,
			"requestParameters": map[string]any{"groupId": "sg-0db", "ipPermissions": map[string]any{},
				"securityGroupRuleIds": map[string]any{"items": []any{map[string]any{"securityGroupRuleId": "sgr-0d4"}}}},
			"responseElements": map[string]any{"requestId": "6f3c", "_return": true,
				"revokedSecurityGroupRuleSet": map[string]any{"items": []any{map[string]any{
					"groupId": "sg-0db", "securityGroupRuleId": "sgr-0d4", "isEgress": false,
					"ipProtocol": "tcp", "fromPort": 5432, "toPort": 5432, "cidrIpv4": "10.42.64.0/18",
				}}}},
			"userIdentity": map[string]any{"type": "IAMUser", "userName": "shop-operator"},
		}),
		awsTrailEvent(t, "DescribeSecurityGroups", "true", "", map[string]any{
			"readOnly": true, "errorCode": "UnauthorizedOperation",
			"responseElements": map[string]any{"securityGroupInfo": "x"},
		}),
	))
	if len(ans.Events) != 2 {
		t.Fatalf("events = %+v", ans.Events)
	}
	resp := string(ans.Events[0].Response)
	for _, want := range []string{`"cidrIpv4":"10.42.64.0/18"`, `"fromPort":5432`, `"ipProtocol":"tcp"`} {
		if !strings.Contains(resp, want) {
			t.Errorf("response lacks %s: %s", want, resp)
		}
	}
	if strings.Contains(resp, "requestId") || strings.Contains(resp, "_return") {
		t.Errorf("response keeps the noise: %s", resp)
	}
	if ans.Events[1].Response != nil {
		t.Errorf("a read got a response: %s", ans.Events[1].Response)
	}
	if notes := strings.Join(awsTrailNotes(ans, 0), "\n"); !strings.Contains(notes, awsTrailPreviousNote) {
		t.Errorf("notes = %s", notes)
	}

	// A change whose answer is only the noise keeps no response.
	bare, _, _ := awsCompactTrail(awsTrailLookupReply(t, "", awsTrailEvent(t, "DeleteSecurityGroup", "false", "", map[string]any{
		"readOnly": false, "requestParameters": map[string]any{"groupId": "sg-1"},
		"responseElements": map[string]any{"requestId": "r", "_return": true},
	})))
	if bare.Events[0].Response != nil {
		t.Errorf("noise-only response kept: %s", bare.Events[0].Response)
	}
}

func TestAWSCompactTrailHidesCredentials(t *testing.T) {
	ans, _, _ := awsCompactTrail(awsTrailLookupReply(t, "",
		awsTrailEvent(t, "CreateKeyPair", "false", "", map[string]any{
			"readOnly": false, "requestParameters": map[string]any{"keyName": "lab"},
			"responseElements": map[string]any{"keyName": "lab", "keyFingerprint": "1f:51", "keyMaterial": "-----BEGIN RSA PRIVATE KEY-----"},
		}),
		awsTrailEvent(t, "AssumeRole", "false", "", map[string]any{
			"readOnly": false, "requestParameters": map[string]any{"roleArn": "arn:aws:iam::111122223333:role/x"},
			"responseElements": map[string]any{"credentials": map[string]any{"accessKeyId": "ASIA1", "sessionToken": "IQoJb3"}},
		}),
		awsTrailEvent(t, "ModifyDBInstance", "false", "", map[string]any{
			"readOnly": false, "requestParameters": map[string]any{"dBInstanceIdentifier": "pg", "masterUserPassword": "hunter2"},
		}),
	))
	raw, _ := json.Marshal(ans)
	for _, gone := range []string{"BEGIN RSA", "IQoJb3", "ASIA1", "hunter2"} {
		if strings.Contains(string(raw), gone) {
			t.Errorf("%q reached the model: %s", gone, raw)
		}
	}
	for _, kept := range []string{`"keyFingerprint":"1f:51"`, `"credentials":"[REDACTED]"`, `"masterUserPassword":"[REDACTED]"`, `"dBInstanceIdentifier":"pg"`} {
		if !strings.Contains(string(raw), kept) {
			t.Errorf("missing %s in %s", kept, raw)
		}
	}
}

func TestAWSCompactTrailClipsLargeResponses(t *testing.T) {
	ans, _, _ := awsCompactTrail(awsTrailLookupReply(t, "", awsTrailEvent(t, "RunInstances", "false", "", map[string]any{
		"readOnly": false, "requestParameters": map[string]any{"instanceType": "t4g.nano"},
		"responseElements": map[string]any{"instancesSet": strings.Repeat("y", awsTrailResponseMax*3)},
	})))
	var s string
	if json.Unmarshal(ans.Events[0].Response, &s) != nil || len(s) > awsTrailResponseMax || !strings.HasSuffix(s, "…") {
		t.Fatalf("response kept %d bytes: %.80s", len(ans.Events[0].Response), ans.Events[0].Response)
	}
}

func TestAWSBindingCompactsLookupEvents(t *testing.T) {
	rig := newAWSRig(t)
	ret := awsTrailFixture(t)
	reply, err := json.Marshal(map[string]any{
		"status": "success", "return_value": ret,
		"api_calls": []map[string]string{{"service": "cloudtrail", "operation": "LookupEvents", "status": "success"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rig.fake.with(func(f *fakeAWSMCP) {
		f.runScript = func(svc, op, region, params string) (string, bool) { return string(reply), false }
	})
	b := rig.bind(t, awsModeAmbient)
	res, err := b.Call(context.Background(), ConsumerKobi, awsToolCall,
		callArgs("cloudtrail", "LookupEvents", `{"LookupAttributes":[{"AttributeKey":"ResourceName","AttributeValue":"sg-0db"}]}`, ""))
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	if strings.Contains(res.Text, "CloudTrailEvent") || !strings.Contains(res.Text, `"event":"RevokeSecurityGroupIngress"`) {
		t.Fatalf("text = %s", res.Text)
	}
	if len(res.Notes) == 0 || res.Notes[0] != awsTrailKeptNote+" It left out 1 read-only calls that succeeded (Describe, List, Get)." {
		t.Fatalf("notes = %q", res.Notes)
	}

	// Any other operation comes back as before.
	rig.fake.with(func(f *fakeAWSMCP) { f.runScript = nil })
	res, err = b.Call(context.Background(), ConsumerKobi, awsToolCall, callArgs("rds", "DescribeDBInstances", "", ""))
	if err != nil || res.IsError || len(res.Notes) != 0 {
		t.Fatalf("rds call: %v %+v", err, res)
	}
}
