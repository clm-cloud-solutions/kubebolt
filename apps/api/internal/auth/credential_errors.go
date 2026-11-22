package auth

import "errors"

// IsCredentialRejection reports whether err is a verdict on the credential
// itself — malformed, unknown, revoked or expired, owned by a disabled or
// deleted org, not accepted on this path — as opposed to a failure to reach a
// verdict, which is what a store that does not answer returns.
//
// Callers answer a rejection with 401/403 and anything else with 503. The
// difference is whether the client retries: Prometheus drops a remote_write
// batch refused with 401 and retries a 5xx, and an API key's caller reads 401
// as "my key is bad". A Postgres restart used to answer every token with 401.
func IsCredentialRejection(err error) bool {
	for _, verdict := range []error{
		ErrTokenMalformed, ErrTokenNotFound, ErrTokenRevoked, ErrTokenExpired,
		ErrTenantDisabled, ErrTenantNotFound,
		ErrTokenEdgeBlocked, errAPITokensUnavailable,
		ErrMissingToken, ErrTokenInvalid, ErrTLSRequired, ErrUnknownMode,
	} {
		if errors.Is(err, verdict) {
			return true
		}
	}
	return false
}
