//go:build !ee

package auth

import (
	"context"
	"errors"
)

// BootstrapOrg is the OSS stub for the multi-org signup core. OSS is single-org
// (one auto-seeded "default" tenant), so self-service org provisioning is an
// EE-only capability — the real implementation lives in org_bootstrap_ee.go
// behind the `ee` build tag. This stub exists only so OSS compiles: the Signup
// handler short-circuits with 409 requires_ee before ever reaching it (the
// MultiTenantEnabled seam is false in OSS), so this is never invoked at runtime.
func BootstrapOrg(_ context.Context, _ TenantStore, _ TeamStore, _ UserStore,
	_, _, _, _, _ string) (*Tenant, *User, error) {
	return nil, nil, errors.New("self-service signup requires the multi-org edition")
}
