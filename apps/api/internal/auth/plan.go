package auth

import "fmt"

// Plan tiers. Plan is stored free-text on the Tenant; these are the recognized
// values, mirroring PLAN_TIERS in
// apps/web/src/pages/platform/PlatformOrgModal.tsx — keep the two in sync.
// An empty or unrecognized plan is treated as Free downstream (the cheapest,
// safest floor — see IsFreePlan).
const (
	PlanFree         = "free"
	PlanTeam         = "team"
	PlanBusiness     = "business"
	PlanEnterprise   = "enterprise"
	PlanEESelfHosted = "ee_self_hosted"
)

// knownPlans is the set ValidatePlan accepts (besides empty).
var knownPlans = map[string]bool{
	PlanFree:         true,
	PlanTeam:         true,
	PlanBusiness:     true,
	PlanEnterprise:   true,
	PlanEESelfHosted: true,
}

// IsFreePlan reports whether an org on this plan gets the Free model band.
// Empty AND unrecognized plans return true — a typo or legacy value falls to
// the cheap floor, never accidentally to a premium band. So the model gate
// stays safe even if a non-canonical plan string slips past write validation.
func IsFreePlan(plan string) bool {
	switch plan {
	case PlanTeam, PlanBusiness, PlanEnterprise, PlanEESelfHosted:
		return false
	}
	return true
}

// ValidatePlan rejects a plan that isn't a recognized tier. Empty is allowed
// (means "unset" → treated as Free). Mirrors the frontend PLAN_TIERS list.
func ValidatePlan(plan string) error {
	if plan == "" || knownPlans[plan] {
		return nil
	}
	return fmt.Errorf("unknown plan %q", plan)
}
