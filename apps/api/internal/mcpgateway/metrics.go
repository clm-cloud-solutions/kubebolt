package mcpgateway

import (
	"errors"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
)

// metrics are the gateway's Prometheus series. Labels stay bounded: the
// connector is a catalog id and the tool a local name the catalog allows —
// a name the model invented is counted as "unlisted", never as itself.
type metrics struct {
	calls    *prometheus.CounterVec
	duration *prometheus.HistogramVec
	bytes    *prometheus.HistogramVec
}

func newMetrics(reg prometheus.Registerer, log *slog.Logger) *metrics {
	m := &metrics{
		calls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kubebolt_mcp_calls_total",
			Help: "External MCP tool calls through the gateway, by connector, tool and result.",
		}, []string{"connector", "tool", "result"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "kubebolt_mcp_call_duration_seconds",
			Help: "Duration of external MCP tool calls, by connector.",
			// AWS's first call of a session starts a sandbox (6-9 s).
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 4, 8, 15, 30, 60},
		}, []string{"connector"}),
		bytes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "kubebolt_mcp_response_bytes",
			Help:    "Size of external MCP tool responses before redaction and truncation, by connector.",
			Buckets: prometheus.ExponentialBuckets(256, 4, 8), // 256 B … 4 MiB
		}, []string{"connector"}),
	}
	if reg != nil {
		m.calls = registerOrReuse(reg, m.calls, log)
		m.duration = registerOrReuse(reg, m.duration, log)
		m.bytes = registerOrReuse(reg, m.bytes, log)
	}
	return m
}

// registerOrReuse registers c, or adopts the collector already registered
// under the same name: a second gateway in the same process (tests, a
// rebuilt router) must count into the same series, not panic.
func registerOrReuse[T prometheus.Collector](reg prometheus.Registerer, c T, log *slog.Logger) T {
	err := reg.Register(c)
	if err == nil {
		return c
	}
	var are prometheus.AlreadyRegisteredError
	if errors.As(err, &are) {
		if existing, ok := are.ExistingCollector.(T); ok {
			return existing
		}
	}
	log.Warn("mcp gateway: metric not registered", slog.String("error", err.Error()))
	return c
}

func (m *metrics) observe(connector, tool, result string, seconds float64, bytes int, called bool) {
	m.calls.WithLabelValues(connector, tool, result).Inc()
	if called {
		m.duration.WithLabelValues(connector).Observe(seconds)
		m.bytes.WithLabelValues(connector).Observe(float64(bytes))
	}
}
