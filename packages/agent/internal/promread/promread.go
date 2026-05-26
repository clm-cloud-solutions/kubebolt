// Package promread is the agent-side collector that pulls metrics
// from the customer's Prometheus via HTTP (Mode C of the Universal
// Data Plane Plan — see internal/agent-universal-data-plane-plan.md
// § Phase 6 and internal/roadmap-1.13.md for the broader design).
//
// Instead of scraping targets directly like cadvisor / kubelet do,
// promread fires periodic /api/v1/query_range requests against the
// customer's existing Prom and translates the response into the
// same agentv2.Sample shape the rest of the agent ships through
// the existing buffer → AgentChannel pipeline. The backend does
// not differentiate Mode A (sidecar scrape) from Mode C (read) —
// the receiver-side code consumes the same wire format.
//
// Scope of this package in S1 of the 1.13 cycle: scaffolding +
// auth (none / basicAuth / bearer) + PromQL client + converter.
// The agent bootstrap wiring (main loop integration) and managed-
// cloud auth providers (SigV4, Azure WI, GCP IAM) land in
// subsequent chunks.
package promread

import (
	"context"
	"errors"
	"fmt"
	"time"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// Defaults applied when corresponding Config fields are zero-valued.
const (
	DefaultPollInterval = 30 * time.Second
	DefaultStep         = 30 * time.Second
	DefaultLookback     = 60 * time.Second
)

// Config holds the operator-set knobs for reading from the customer's
// Prometheus. Mirrors the kubebolt-agent helm values block
// `agent.promRead.*` (chart wiring lands in a follow-up chunk).
type Config struct {
	// Enabled gates the whole reader at boot. Mutually exclusive
	// with `scrape.enabled` — that check is enforced at chart
	// render time (hard-fail), not here.
	Enabled bool

	// URL is the customer Prom endpoint (e.g.
	// "http://prometheus.monitoring:9090"). Trailing slashes are
	// tolerated; /api/v1/query_range is appended by the client.
	URL string

	// Auth is the resolved provider config. See auth.go for the
	// Provider interface and concrete impls. S1 ships none /
	// basicAuth / bearer; S2 adds AwsSigV4 / AzureWorkloadIdentity
	// / GcpIam.
	Auth AuthConfig

	// PollInterval is the cadence at which the reader fires a new
	// query_range against the customer's Prom. Default 30s. Lower
	// values increase query load on the customer's Prom — the S1
	// acceptance criteria caps it at "≈ one extra Grafana client".
	PollInterval time.Duration

	// Step is the Prom query_range `step` parameter (resolution of
	// the returned matrix). Default 30s. Align to the customer's
	// scrape_interval to avoid sub-sample interpolation.
	Step time.Duration

	// Lookback is how far back each query_range window extends from
	// "now". Should be >= PollInterval + small slack so consecutive
	// polls overlap and gap-recover from transient Prom failures.
	Lookback time.Duration

	// Matchers is the selectivity list. Each entry is a PromQL
	// selector like `{__name__=~"kube_pod_.*"}`. An empty list is
	// rejected at Validate time — pulling "everything" by accident
	// is the most expensive single mistake an operator can make
	// here.
	Matchers []string

	// ClusterID, ClusterName, TenantID are stamped on every sample's
	// Labels map. Same convention as the cadvisor collector.
	ClusterID   string
	ClusterName string
	TenantID    string
}

// Validate returns nil when the config is internally consistent
// enough that NewReader can construct a working Reader. Skipped
// entirely when Enabled=false.
func (c *Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.URL == "" {
		return errors.New("promread: URL is required when Enabled=true")
	}
	if len(c.Matchers) == 0 {
		return errors.New("promread: at least one Matcher is required when Enabled=true")
	}
	if _, err := NewProvider(c.Auth); err != nil {
		return fmt.Errorf("promread: %w", err)
	}
	return nil
}

// applyDefaults fills in zero-valued duration fields with the
// package defaults. Mutates the receiver — invoked from NewReader.
func (c *Config) applyDefaults() {
	if c.PollInterval <= 0 {
		c.PollInterval = DefaultPollInterval
	}
	if c.Step <= 0 {
		c.Step = DefaultStep
	}
	if c.Lookback <= 0 {
		c.Lookback = DefaultLookback
	}
}

// Reader pulls samples from the customer's Prometheus on a fixed
// cadence, converts them to the agent's wire format, and returns
// them via Collect. The caller (agent main loop, wired in a
// follow-up chunk) is responsible for pushing the results into the
// buffer.Ring.
type Reader struct {
	cfg    Config
	client *Client
}

// NewReader constructs a Reader. Returns an error when cfg fails
// Validate.
func NewReader(cfg Config) (*Reader, error) {
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	auth, err := NewProvider(cfg.Auth)
	if err != nil {
		return nil, err
	}
	return &Reader{
		cfg:    cfg,
		client: NewClient(cfg.URL, auth),
	}, nil
}

// Collect fires one query_range per configured Matcher against the
// customer's Prom and returns the union of converted samples.
// Errors from individual matchers are captured in the first non-nil
// error returned, but partial results from successful matchers are
// always returned — the caller decides whether the batch is worth
// shipping despite a partial failure.
func (r *Reader) Collect(ctx context.Context) ([]*agentv2.Sample, error) {
	end := time.Now()
	start := end.Add(-r.cfg.Lookback)

	var all []*agentv2.Sample
	var firstErr error
	for _, matcher := range r.cfg.Matchers {
		resp, err := r.client.QueryRange(ctx, matcher, start, end, r.cfg.Step)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("query %q: %w", matcher, err)
			}
			continue
		}
		samples, err := Convert(resp, r.cfg.ClusterID, r.cfg.ClusterName, r.cfg.TenantID)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("convert %q: %w", matcher, err)
			}
			continue
		}
		all = append(all, samples...)
	}
	return all, firstErr
}

// PollInterval is exposed so the agent's run loop can pick the
// cadence without re-reading the Config (which stays unexported).
func (r *Reader) PollInterval() time.Duration {
	return r.cfg.PollInterval
}
