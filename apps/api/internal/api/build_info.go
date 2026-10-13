package api

import "github.com/prometheus/client_golang/prometheus"

// RegisterBuildInfo publishes kubebolt_build_info{version,edition} = 1. With
// the instance label the self-push adds, it answers how many API replicas are
// reporting, and which version each one runs during a rollout; its absence is
// how the operator alerts notice a replica that stopped reporting.
func RegisterBuildInfo(reg prometheus.Registerer, version, edition string) {
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "kubebolt_build_info",
		Help:        "KubeBolt API build: always 1, labelled with the version and edition.",
		ConstLabels: prometheus.Labels{"version": version, "edition": edition},
	})
	g.Set(1)
	if reg != nil {
		reg.MustRegister(g)
	}
}
