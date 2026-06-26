package auth

import "testing"

func TestIsFreePlan(t *testing.T) {
	free := []string{"free", "", "pro", "legacy-typo"} // empty + unrecognized → Free floor
	paid := []string{"team", "business", "enterprise", "ee_self_hosted"}
	for _, p := range free {
		if !IsFreePlan(p) {
			t.Errorf("IsFreePlan(%q) = false, want true (free floor)", p)
		}
	}
	for _, p := range paid {
		if IsFreePlan(p) {
			t.Errorf("IsFreePlan(%q) = true, want false (paid)", p)
		}
	}
}

func TestValidatePlan(t *testing.T) {
	for _, p := range []string{"", "free", "team", "business", "enterprise", "ee_self_hosted"} {
		if err := ValidatePlan(p); err != nil {
			t.Errorf("ValidatePlan(%q) = %v, want nil", p, err)
		}
	}
	for _, p := range []string{"pro", "Free", "enterprise ", "bogus"} {
		if err := ValidatePlan(p); err == nil {
			t.Errorf("ValidatePlan(%q) = nil, want error", p)
		}
	}
}
