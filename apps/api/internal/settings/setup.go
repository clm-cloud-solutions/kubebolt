package settings

import (
	"fmt"
)

// setupCompleteKey marks "first-login wizard finished" so the UI knows
// whether to overlay the welcome flow. One boolean, stored alongside
// the other Settings records in the same BoltDB bucket.
//
// Semantics:
//   - Absent / false → fresh install or wizard not yet completed. The
//     admin Layout shows the setup wizard on every page load until the
//     admin runs through it (or explicitly dismisses to "skip").
//   - True → wizard done. Settings tabs are the path forward.
//
// Marking is intentionally one-way through the API (POST /admin/setup/
// complete). Operators who want to re-run can clear via the same
// POST endpoint with `?reset=true` — useful for tutorial demos and
// per-cluster onboarding videos.
const setupCompleteKey = "setup_complete"

// IsSetupComplete returns true if the wizard has been finished at
// least once. A missing record reads as false (fresh install).
//
// Cheap: single BoltDB read on the settings bucket, no decryption.
// Safe to call on every page load.
func (r *Runtime) IsSetupComplete() bool {
	raw, err := r.store.GetSetting(setupCompleteKey)
	if err != nil {
		return false // not found = not complete
	}
	// Stored as the literal byte "1" for true. Any other value falls
	// through as false, including an explicit "0" written by reset.
	return string(raw) == "1"
}

// MarkSetupComplete writes the done flag. Idempotent — calling twice
// is fine; the second call is a no-op write of the same byte.
func (r *Runtime) MarkSetupComplete() error {
	if err := r.store.SetSetting(setupCompleteKey, []byte("1")); err != nil {
		return fmt.Errorf("persist setup_complete: %w", err)
	}
	return nil
}

// ResetSetup clears the done flag, returning the install to "show the
// wizard" state. Used by operators rerunning onboarding for demos /
// docs / per-cluster setup verification.
func (r *Runtime) ResetSetup() error {
	if err := r.store.SetSetting(setupCompleteKey, []byte("0")); err != nil {
		return fmt.Errorf("reset setup_complete: %w", err)
	}
	return nil
}
