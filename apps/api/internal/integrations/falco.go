package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Falco integration (E2 SEC-E) — runtime threat detection. Decision B
// (2026-07-13, reversal criteria recorded in the E2 wave plan):
// falcosidekick POSTs each event STRAIGHT to the control plane's
// bearer-authenticated ingest endpoint — no agent sink. The Normalize
// contract is receiver-agnostic on purpose: if intense testing flips
// the decision to the agent-sink option, the provider, store and
// dashboard stay untouched; only who accepts the POST moves.
//
// falcosidekick config on the cluster:
//
//	webhook:
//	  address: https://<kubebolt>/api/v1/ingest/falco
//	  customHeaders: "Authorization:Bearer <ingest-token>"
const (
	FalcoID   = "falco"
	FalcoName = "Falco"

	falcoLabelSelector = "app.kubernetes.io/name=falco"
)

var falcoFallbackNamespaces = []string{"falco", "falco-system"}

type falcoProvider struct{}

// NewFalco constructs the Falco integration provider.
func NewFalco() Provider { return &falcoProvider{} }

var _ SignalProvider = (*falcoProvider)(nil)

func (p *falcoProvider) Meta() Integration {
	return Integration{
		ID:          FalcoID,
		Name:        FalcoName,
		Description: "Runtime threat detection. falcosidekick pushes rule hits to KubeBolt's ingest endpoint; they surface as the Runtime Threats feed on Security & Compliance. Needs an ingest token SCOPED TO A CLUSTER — a pushed event carries no other identity, so an unscoped token is refused.",
		DocsURL:     "https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/falco.md",
		Capabilities: []string{
			"security.runtime",
		},
	}
}

func (p *falcoProvider) Detect(ctx context.Context, cs kubernetes.Interface) (Integration, error) {
	meta := p.Meta()
	if cs == nil {
		meta.Status = StatusUnknown
		meta.Health = &Health{Message: "no cluster connection"}
		return meta, nil
	}
	pods, err := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		LabelSelector: falcoLabelSelector,
		Limit:         64, // DaemonSet — one per node
	})
	if err != nil {
		meta.Status = StatusUnknown
		meta.Health = &Health{Message: fmt.Sprintf("could not list pods: %v", err)}
		return meta, nil
	}
	items := pods.Items
	if len(items) == 0 {
		for _, ns := range falcoFallbackNamespaces {
			nsPods, nsErr := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{Limit: 64})
			if nsErr != nil {
				continue
			}
			for i := range nsPods.Items {
				if strings.HasPrefix(nsPods.Items[i].Name, "falco") {
					items = append(items, nsPods.Items[i])
				}
			}
			if len(items) > 0 {
				break
			}
		}
	}
	if len(items) == 0 {
		meta.Status = StatusNotInstalled
		return meta, nil
	}
	first := items[0]
	meta.Namespace = first.Namespace
	meta.Version = first.Labels["app.kubernetes.io/version"]
	health := &Health{}
	for i := range items {
		if items[i].Namespace != first.Namespace {
			continue
		}
		health.PodsDesired++
		if isPodReady(&items[i]) {
			health.PodsReady++
		}
	}
	meta.Health = health
	if health.PodsReady == 0 || health.PodsReady < health.PodsDesired {
		meta.Status = StatusDegraded
		health.Message = "Falco pods not fully ready"
		return meta, nil
	}
	meta.Status = StatusInstalled
	return meta, nil
}

func (p *falcoProvider) IngestMode() IngestPattern { return IngestHTTPSink }

// falcoSidekickEvent is falcosidekick's webhook payload for ONE rule
// hit.
type falcoSidekickEvent struct {
	Output       string                 `json:"output"`
	Priority     string                 `json:"priority"`
	Rule         string                 `json:"rule"`
	Time         time.Time              `json:"time"`
	OutputFields map[string]interface{} `json:"output_fields"`
	Hostname     string                 `json:"hostname"`
	Source       string                 `json:"source"`
	// Tags carry the rule's classification, and among them the MITRE ATT&CK
	// technique — `T1555`, `mitre_credential_access`. For whoever triages a
	// runtime alert that is the most useful line in the payload: it says what
	// the attacker was going for, not just what syscall fired. It was being
	// dropped on the floor.
	Tags []string `json:"tags"`
}

// Normalize turns one falcosidekick webhook body ([]byte) into a
// RuntimeEvent. Pure and receiver-agnostic — the ingest endpoint
// (Decision B) and a future agent sink (Decision A) both feed it the
// same payload.
func (p *falcoProvider) Normalize(ctx context.Context, raw any) (Signals, error) {
	body, ok := raw.([]byte)
	if !ok {
		return Signals{}, fmt.Errorf("falco normalize: want []byte webhook body, got %T", raw)
	}
	var ev falcoSidekickEvent
	// UseNumber, not plain Unmarshal: output_fields is map[string]interface{},
	// so every number decodes to float64 and `fmt.Sprint` then renders big ones
	// in scientific notation. `evt.time` is int64 nanoseconds and was being
	// stored as "1.7860402125341727e+18" — a value you cannot get the exact
	// nanosecond back out of. json.Number keeps the literal the sender wrote.
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&ev); err != nil {
		return Signals{}, fmt.Errorf("falco normalize: %w", err)
	}
	if ev.Rule == "" && ev.Output == "" {
		return Signals{}, fmt.Errorf("falco normalize: payload has neither rule nor output")
	}

	at := ev.Time
	if at.IsZero() {
		at = time.Now().UTC()
	}
	// The k8s identity rides output_fields when Falco's k8s metadata
	// enrichment is on. Absent on host-scoped rules — fine, the feed
	// shows the hostname instead.
	fields := make(map[string]string, len(ev.OutputFields))
	for k, v := range ev.OutputFields {
		fields[k] = fmt.Sprint(v)
	}
	ns := fields["k8s.ns.name"]
	pod := fields["k8s.pod.name"]
	if ev.Hostname != "" {
		fields["hostname"] = ev.Hostname
	}
	// Empties filtered: falcosidekick appends its own `customtags`, and when
	// that is unset the joined string arrives with a leading comma.
	tags := make([]string, 0, len(ev.Tags))
	for _, t := range ev.Tags {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	if len(tags) > 0 {
		fields["tags"] = strings.Join(tags, ",")
	}

	return Signals{Events: []RuntimeEvent{{
		At:               at.UTC(),
		Priority:         strings.Title(strings.ToLower(ev.Priority)),
		RuleName:         ev.Rule,
		Namespace:        ns,
		PodName:          pod,
		DetectedBehavior: ev.Output,
		Source:           FalcoID,
		Fields:           fields,
	}}}, nil
}
