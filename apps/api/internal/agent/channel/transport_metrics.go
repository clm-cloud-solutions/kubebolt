package channel

import (
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// The agent-proxy's own series for System › Health: how many agent channels
// this replica holds, and what a request through the tunnel costs. No cluster
// label: they describe KubeBolt itself, not a cluster.

// Results of one unary request through the tunnel. "ok" means the agent
// answered — whatever HTTP status the apiserver gave (a 404 is the tunnel
// working); the rest are the tunnel failing.
const (
	tunnelOK           = "ok"
	tunnelTimeout      = "timeout"
	tunnelAgentClosed  = "agent_closed"
	tunnelCanceled     = "canceled"
	tunnelNotConnected = "not_connected"
	tunnelSendError    = "send_error"
)

var kubeRequestSeconds atomic.Pointer[prometheus.HistogramVec]

// RegisterTransportMetrics registers the tunnel histogram and the live-channel
// gauge on reg. Called once at boot; before it (and in tests) RoundTrip
// records nothing.
func RegisterTransportMetrics(reg prometheus.Registerer, registry *AgentRegistry) {
	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "kubebolt_agent_kube_request_seconds",
		Help:    "Duration of unary Kubernetes API requests through the agent tunnel, by result (ok, timeout, agent_closed, canceled, not_connected, send_error). Watches and exec/port-forward tunnels are not timed.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30},
	}, []string{"result"})
	channels := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "kubebolt_agent_channels",
		Help: "Live agent gRPC channels held by this API replica. A channel is pinned to the replica it connected to.",
	}, func() float64 {
		if registry == nil {
			return 0
		}
		return float64(registry.Count())
	})
	if reg != nil {
		reg.MustRegister(h, channels)
	}
	kubeRequestSeconds.Store(h)
}

func observeKubeRequest(result string, start time.Time) {
	if h := kubeRequestSeconds.Load(); h != nil {
		h.WithLabelValues(result).Observe(time.Since(start).Seconds())
	}
}
