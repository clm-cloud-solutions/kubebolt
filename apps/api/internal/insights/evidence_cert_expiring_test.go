package insights

import (
	"testing"
	"time"
)

// cert-expiring: when it expires (or expired), the issuer, its Ready status
// and when cert-manager means to renew it.
func TestCertExpiring_EvidenceCarriesExpiryIssuerAndRenewal(t *testing.T) {
	notAfter := time.Now().Add(5 * 24 * time.Hour).UTC().Truncate(time.Second)
	cert := map[string]interface{}{
		"name": "api-tls", "namespace": "shop", "expiresInDays": 5,
		"notAfter": notAfter.Format(time.RFC3339), "issuer": "letsencrypt-prod", "status": "Ready",
		"renewalTime": "2026-10-14T00:00:00Z",
	}
	got := certExpiringRule().Evaluate(&ClusterState{Certificates: []map[string]interface{}{cert}})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if e := evidenceByLabel(t, got[0], "Not after"); e.Detail != "expires in 5 day(s)" || e.At == nil || !e.At.Equal(notAfter) {
		t.Errorf("not after: %+v", e)
	}
	if d := evidenceByLabel(t, got[0], "Issuer").Detail; d != "letsencrypt-prod" {
		t.Errorf("issuer: %q", d)
	}
	evidenceByLabel(t, got[0], "Renewal time")
}
