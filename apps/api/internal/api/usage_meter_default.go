//go:build !ee

package api

import "net/http"

// meterAPIRequest is an identity pass-through in OSS: per-org API-request
// metering is a multi-tenant billing dimension with no meaning in single-tenant.
// The EE build (usage_meter_ee.go) supplies the real middleware.
func (h *handlers) meterAPIRequest(next http.Handler) http.Handler { return next }
