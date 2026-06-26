package agent

import agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"

// enforceTenantLabel reconciles a metric batch's tenant_id labels against the
// agent's authenticated tenant, mirroring the remote_write gate in
// prom_write.go. It is the gRPC StreamMetrics half of the same anti-spoofing
// policy — without it a compromised Mode-A agent could stamp another org's
// tenant_id and write into its series (the two ingest paths must enforce the
// same boundary).
//
// Three cases per sample, same as remote_write:
//   - tenant_id present and != tenantID → SPOOF. Returns (asserted, false);
//     the caller rejects the batch and tears the stream down. The mode does
//     not matter for a mismatch — it is an active attack, never permissive.
//   - tenant_id absent → stamped authoritatively with tenantID, in place. The
//     backend knows the true tenant from the authenticated identity, so this
//     can't be spoofed; it covers agents that don't self-stamp.
//   - tenant_id present and == tenantID → already correct, left untouched.
//
// OSS-neutral: an empty tenantID (auth disabled / no authenticated tenant)
// short-circuits to (",", true) with no inspection or stamping, so stock OSS
// ships samples exactly as before.
//
// On a mismatch the function stops at the first offending sample and returns
// immediately; any samples it stamped before that point are irrelevant because
// the caller drops the whole batch.
func enforceTenantLabel(samples []*agentv2.Sample, tenantID string) (asserted string, ok bool) {
	if tenantID == "" {
		return "", true
	}
	for _, s := range samples {
		if s == nil {
			continue
		}
		if v, has := s.GetLabels()[TenantIDLabel]; has {
			if v != tenantID {
				return v, false
			}
			continue
		}
		if s.Labels == nil {
			s.Labels = map[string]string{}
		}
		s.Labels[TenantIDLabel] = tenantID
	}
	return "", true
}

// TenantIDLabel is the series label that scopes a sample to its owning org.
// It is the gRPC-side spelling of the same label the remote_write path stamps
// (TenantIDLabelName in api/prom_write_injector.go) and that VM read queries
// filter on — they MUST stay identical or ingest and read disagree.
const TenantIDLabel = "tenant_id"
