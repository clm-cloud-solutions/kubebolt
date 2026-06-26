package api

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// poolStatsSource is the slice of *cluster.Manager the pool metrics need.
type poolStatsSource interface {
	PoolStats() cluster.PoolStats
}

// PoolMetricsCollector exposes the connector-pool size on /metrics (always-on,
// W2 §10.3) so operators can watch the resident-runtime cost — how many cluster
// runtimes the backend keeps live as agents connect. Collected on each scrape
// (always fresh; no background ticker).
type PoolMetricsCollector struct {
	src  poolStatsSource
	desc *prometheus.Desc
}

// NewPoolMetricsCollector registers the pool collector on reg (pass
// prometheus.DefaultRegisterer in production; a fresh registry in tests).
func NewPoolMetricsCollector(reg prometheus.Registerer, src poolStatsSource) *PoolMetricsCollector {
	c := &PoolMetricsCollector{
		src: src,
		desc: prometheus.NewDesc(
			"kubebolt_api_runtimes",
			"Cluster runtimes resident in the connector pool, by state (always-on, W2 §10).",
			[]string{"state"}, nil,
		),
	}
	if reg != nil {
		reg.MustRegister(c)
	}
	return c
}

func (c *PoolMetricsCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *PoolMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.src.PoolStats()
	ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(s.Active), "active")
	ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(s.Parked), "parked")
	ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(s.Building), "building")
}
