// Package costmodel turns capacity into money, the same way the web does.
//
// The formula used to exist only in the browser (estimateMonthlySavings in
// apps/web hooks/useClusterCost.ts): reclaimable cores and GiB at the
// cluster's average node rates, over OpenCost's 730-hour month. Kobi Plans
// need the number on the backend, so it lives here; with the same formula and
// the same rates the plan and the Capacity strip say the same figure (Kobi
// Plans spec, «Dinero: función nueva en el backend»).
package costmodel

import "math"

// HoursPerMonth is OpenCost's convention for projecting an hourly price.
const HoursPerMonth = 730

const gib = 1024 * 1024 * 1024

// NodeRates are a cluster's average hourly prices per core and per GiB, from
// OpenCost's node_cpu_hourly_cost and node_ram_hourly_cost, in OpenCost's
// currency.
type NodeRates struct {
	CPUCoreHourly float64
	RAMGiBHourly  float64
}

// Available reports whether OpenCost sent any price. Without one there is no
// money figure: a price is never estimated.
func (r NodeRates) Available() bool { return r.CPUCoreHourly > 0 || r.RAMGiBHourly > 0 }

// MonthlySavings is what reclaiming this capacity is worth per month at these
// rates: (cores × CPU rate + GiB × RAM rate) × 730.
func MonthlySavings(cpuMilli, memBytes int64, r NodeRates) float64 {
	cores := float64(cpuMilli) / 1000
	gibs := float64(memBytes) / gib
	return (cores*r.CPUCoreHourly + gibs*r.RAMGiBHourly) * HoursPerMonth
}

// Round2 rounds money to cents.
func Round2(v float64) float64 { return math.Round(v*100) / 100 }
