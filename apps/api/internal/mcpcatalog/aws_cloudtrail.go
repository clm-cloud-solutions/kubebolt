package mcpcatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// CloudTrail's event history, as one compact row per event.
//
// LookupEvents hands every event back twice: a few top-level fields, and the
// whole record again as CloudTrailEvent, a JSON document inside a string of
// 2 to 5 KiB (identity, session context, TLS details, request and response).
// Twenty events filled the answer the model may read, and in an account with
// a load balancer controller or an agent most of them were somebody's
// Describe calls. Azure's activity log had the same shape of problem
// (azure_activitylog.go): the write that mattered was in the log and out of
// reach. KubeBolt keeps who, when, from where, on what, what the call sent,
// what AWS answered to a change and its error, and leaves out the read-only
// calls that succeeded, saying how many. Nothing here knows a service.
//
// The answer matters when the request names a thing only by id: a
// RevokeSecurityGroupIngress by rule id sends the id, and AWS answers with the
// rule it removed (protocol, ports, source). Answers can also carry
// credentials (an STS session token, a key pair's private key), so values
// under those keys are replaced in answers and requests alike.

const (
	awsTrailService   = "cloudtrail"
	awsTrailOperation = "LookupEvents"

	// awsTrailRequestMax bounds a request kept whole.
	awsTrailRequestMax = 2 << 10
	// awsTrailResponseMax bounds an answer kept whole: what a Revoke or a
	// Delete removed fits; a RunInstances answer is mostly noise past it.
	awsTrailResponseMax = 1 << 10
	// awsTrailErrorMax bounds a failed call's message.
	awsTrailErrorMax = 600
	// awsTrailAgentMax bounds a user agent: its head says console, CLI, an
	// SDK or Terraform, the rest is version noise.
	awsTrailAgentMax = 80
)

const awsTrailKeptNote = "KubeBolt kept, for each event, when it happened (UTC), the call, who made it, from where, the resources it names, " +
	"what it sent (request), what AWS answered to a change (response) and its error, newest first."

// awsTrailPreviousNote goes with an answer that holds what a change sent or got back.
const awsTrailPreviousNote = "A request is what the call sent and a response what AWS answered. In a Revoke, Detach, Delete, Disable, Remove or Deregister call, " +
	"what existed before the change is in the request, or in the response when the request names it only by id; " +
	"after a Modify, Update or Put call, the value before is in the previous write to the same resource."

// awsTrailEmptyNote goes with an answer that holds no event.
const awsTrailEmptyNote = "Event history holds this region's management events of the last 90 days, and a recent event can take up to about 15 minutes to appear. " +
	"IAM, CloudFront and Route 53 changes are recorded in us-east-1. S3 object reads and Lambda invocations are data events and never appear here."

const awsTrailMoreNote = "There are more events than these: narrow the window with StartTime and EndTime, or look up by ResourceName or EventName."

// awsTrailRow is one event as the model reads it.
type awsTrailRow struct {
	Time      string          `json:"time"`
	Event     string          `json:"event"`
	Source    string          `json:"source,omitempty"`
	Who       string          `json:"who,omitempty"`
	Via       string          `json:"via,omitempty"`
	From      string          `json:"from,omitempty"`
	Agent     string          `json:"agent,omitempty"`
	Resources []string        `json:"resources,omitempty"`
	Request   json.RawMessage `json:"request,omitempty"`
	Response  json.RawMessage `json:"response,omitempty"`
	Error     string          `json:"error,omitempty"`
	ReadOnly  bool            `json:"readOnly,omitempty"`
}

type awsTrailAnswer struct {
	Events     []awsTrailRow `json:"events"`
	MoreEvents bool          `json:"moreEvents,omitempty"`
}

// awsTrailLookup is the part of a LookupEvents answer KubeBolt reads.
type awsTrailLookup struct {
	Events    *[]awsTrailEntry `json:"Events"`
	NextToken string           `json:"NextToken"`
}

type awsTrailEntry struct {
	EventName   string `json:"EventName"`
	EventTime   string `json:"EventTime"`
	EventSource string `json:"EventSource"`
	Username    string `json:"Username"`
	ReadOnly    string `json:"ReadOnly"`
	Resources   []struct {
		ResourceType string `json:"ResourceType"`
		ResourceName string `json:"ResourceName"`
	} `json:"Resources"`
	CloudTrailEvent string `json:"CloudTrailEvent"`
}

// awsTrailRecord is the part of the CloudTrailEvent document KubeBolt reads.
type awsTrailRecord struct {
	EventTime         string          `json:"eventTime"`
	EventSource       string          `json:"eventSource"`
	SourceIPAddress   string          `json:"sourceIPAddress"`
	UserAgent         string          `json:"userAgent"`
	ReadOnly          *bool           `json:"readOnly"`
	ErrorCode         string          `json:"errorCode"`
	ErrorMessage      string          `json:"errorMessage"`
	RequestParameters json.RawMessage `json:"requestParameters"`
	ResponseElements  json.RawMessage `json:"responseElements"`
	RecipientAccount  string          `json:"recipientAccountId"`
	UserIdentity      struct {
		Type           string `json:"type"`
		ARN            string `json:"arn"`
		AccountID      string `json:"accountId"`
		UserName       string `json:"userName"`
		InvokedBy      string `json:"invokedBy"`
		SessionContext struct {
			SessionIssuer struct {
				UserName string `json:"userName"`
			} `json:"sessionIssuer"`
		} `json:"sessionContext"`
	} `json:"userIdentity"`
}

// awsIsTrailLookup says whether a call is the one this file compacts.
func awsIsTrailLookup(service, operation string) bool {
	return awsNorm(service) == awsTrailService && awsNorm(operation) == awsNorm(awsTrailOperation)
}

// awsCompactTrail turns a LookupEvents answer into one row per event, in the
// order AWS gave them (newest first), without the read-only calls that
// succeeded. reads counts those; ok is false when v is not a LookupEvents
// answer, and the caller then shows it as it came.
func awsCompactTrail(v json.RawMessage) (ans awsTrailAnswer, reads int, ok bool) {
	var l awsTrailLookup
	if json.Unmarshal(v, &l) != nil || l.Events == nil {
		return awsTrailAnswer{}, 0, false
	}
	ans.Events = []awsTrailRow{}
	ans.MoreEvents = l.NextToken != ""
	for _, e := range *l.Events {
		var rec awsTrailRecord
		parsed := e.CloudTrailEvent != "" && json.Unmarshal([]byte(e.CloudTrailEvent), &rec) == nil

		readOnly := strings.EqualFold(e.ReadOnly, "true")
		if parsed && rec.ReadOnly != nil {
			readOnly = *rec.ReadOnly
		}
		// A read that failed stays: an AccessDenied or a disabled key on a
		// Describe or a Decrypt is the symptom, not noise.
		if readOnly && rec.ErrorCode == "" {
			reads++
			continue
		}

		row := awsTrailRow{
			Time:     awsTrailTime(rec.EventTime, e.EventTime),
			Event:    e.EventName,
			Source:   strings.TrimSuffix(awsFirst(rec.EventSource, e.EventSource), ".amazonaws.com"),
			ReadOnly: readOnly,
		}
		if parsed {
			row.Who = awsTrailWho(rec)
			row.Via = rec.UserIdentity.InvokedBy
			row.From = rec.SourceIPAddress
			row.Agent = awsClip(rec.UserAgent, awsTrailAgentMax)
			row.Request = awsTrailRequest(rec.RequestParameters)
			if !readOnly {
				row.Response = awsTrailResponse(rec.ResponseElements)
			}
			if rec.ErrorCode != "" {
				row.Error = awsClip(strings.TrimSuffix(rec.ErrorCode+": "+rec.ErrorMessage, ": "), awsTrailErrorMax)
			}
		}
		if row.Who == "" {
			row.Who = e.Username
		}
		for _, r := range e.Resources {
			if r.ResourceName == "" {
				continue
			}
			row.Resources = append(row.Resources, strings.TrimSpace(r.ResourceType+" "+r.ResourceName))
		}
		ans.Events = append(ans.Events, row)
	}
	return ans, reads, true
}

// awsTrailNotes says what a compacted answer holds and leaves out. Every note
// is a constant or a count of KubeBolt's own.
func awsTrailNotes(ans awsTrailAnswer, reads int) []string {
	kept := awsTrailKeptNote
	if reads > 0 {
		kept += fmt.Sprintf(" It left out %d read-only calls that succeeded (Describe, List, Get).", reads)
	}
	notes := []string{kept}
	if len(ans.Events) == 0 {
		notes = append(notes, awsTrailEmptyNote)
	}
	for _, r := range ans.Events {
		if (len(r.Request) > 0 || len(r.Response) > 0) && !r.ReadOnly {
			notes = append(notes, awsTrailPreviousNote)
			break
		}
	}
	if ans.MoreEvents {
		notes = append(notes, awsTrailMoreNote)
	}
	return notes
}

// awsTrailWho names the identity that made the call: the role and session of
// an assumed role, the IAM user, root, or the service that called on the
// caller's behalf. Another account's identity carries its account id.
func awsTrailWho(rec awsTrailRecord) string {
	id := rec.UserIdentity
	var who string
	switch id.Type {
	case "Root":
		who = "root"
	case "AssumedRole":
		// arn:aws:sts::<account>:assumed-role/<role>/<session>
		if i := strings.Index(id.ARN, ":assumed-role/"); i >= 0 {
			who = "assumed-role/" + id.ARN[i+len(":assumed-role/"):]
		} else if n := id.SessionContext.SessionIssuer.UserName; n != "" {
			who = "assumed-role/" + n
		}
	case "IAMUser":
		who = "user/" + awsFirst(id.UserName, awsARNTail(id.ARN))
	case "AWSService":
		who = id.InvokedBy
	default:
		who = awsFirst(id.UserName, awsARNTail(id.ARN))
	}
	if who == "" {
		who = id.Type
	}
	if id.AccountID != "" && rec.RecipientAccount != "" && id.AccountID != rec.RecipientAccount {
		who += " (account " + id.AccountID + ")"
	}
	return who
}

// awsTrailTime renders an event time as UTC to the second. The record's own
// eventTime is already UTC; boto3 serializes the top-level one with an offset
// and microseconds.
func awsTrailTime(record, top string) string {
	for _, s := range []string{record, top} {
		if s == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return awsFirst(record, top)
}

// awsTrailResponseNoise is what every EC2 answer carries and says nothing.
var awsTrailResponseNoise = []string{"requestId", "_return", "return"}

// awsTrailSecretKeys name values that are credentials wherever they appear:
// STS's credentials and session tokens, a key pair's material, a secret's
// value, a registry token, a password. CloudTrail masks most of them itself;
// KubeBolt does not count on it.
var awsTrailSecretKeys = []string{
	"credentials", "sessionToken", "secretAccessKey", "keyMaterial", "privateKey", "plaintext",
	"secretString", "secretBinary", "authorizationToken", "password", "masterUserPassword",
}

// awsTrailRequest keeps what a call sent, up to awsTrailRequestMax.
func awsTrailRequest(raw json.RawMessage) json.RawMessage {
	return awsTrailPart(raw, nil, awsTrailRequestMax)
}

// awsTrailResponse keeps what AWS answered to a change, up to
// awsTrailResponseMax, without its request id and bare success flag.
func awsTrailResponse(raw json.RawMessage) json.RawMessage {
	return awsTrailPart(raw, awsTrailResponseNoise, awsTrailResponseMax)
}

// awsTrailPart keeps a request or an answer whole up to limit, clipped as text
// past it; nothing for an empty one. Credentials and a function's environment
// variables lose their values first (aws_lambda.go), and a part that cannot be
// checked for them is left out.
func awsTrailPart(raw json.RawMessage, noise []string, limit int) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc any
	if dec.Decode(&doc) != nil {
		return nil
	}
	if m, ok := doc.(map[string]any); ok {
		for k := range m {
			if awsFoldContains(noise, k) {
				delete(m, k)
			}
		}
		if len(m) == 0 {
			return nil
		}
	}
	awsHideIn(doc)
	awsTrailHideSecrets(doc)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(doc) != nil {
		return nil
	}
	out := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if len(out) <= limit {
		return json.RawMessage(out)
	}
	clipped, err := json.Marshal(awsClip(string(out), limit))
	if err != nil {
		return nil
	}
	return json.RawMessage(clipped)
}

// awsTrailHideSecrets replaces, at any depth, the value under a key of
// awsTrailSecretKeys with the mark a function's environment gets.
func awsTrailHideSecrets(node any) {
	switch t := node.(type) {
	case map[string]any:
		for k, child := range t {
			if awsFoldContains(awsTrailSecretKeys, k) {
				t[k] = awsLambdaHiddenValue
				continue
			}
			awsTrailHideSecrets(child)
		}
	case []any:
		for _, child := range t {
			awsTrailHideSecrets(child)
		}
	}
}

func awsFoldContains(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// awsClip shortens s to at most n bytes on a rune boundary, marking the cut.
func awsClip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func awsARNTail(arn string) string {
	if i := strings.LastIndex(arn, "/"); i >= 0 {
		return arn[i+1:]
	}
	return ""
}

func awsFirst(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
