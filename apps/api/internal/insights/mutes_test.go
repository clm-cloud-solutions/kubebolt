package insights

import (
	"strings"
	"testing"
	"time"
)

// ValidateMute is the #54 contract's gate: full key, one expiry mode, and no
// permanent silence without a written reason.
func TestValidateMute(t *testing.T) {
	future := time.Now().Add(7 * 24 * time.Hour)
	past := time.Now().Add(-time.Hour)
	base := Mute{ClusterID: "cl-1", RuleID: "crash-loop", Resource: "Pod/ns/x"}

	cases := []struct {
		name    string
		mutate  func(m *Mute)
		wantErr string // "" = valid
	}{
		{"7d preset", func(m *Mute) { m.ExpiresAt = &future }, ""},
		{"until resolved", func(m *Mute) { m.UntilResolved = true }, ""},
		{"permanent with reason", func(m *Mute) { m.Reason = "known flapper, tracked in #123" }, ""},
		{"permanent without reason", func(m *Mute) {}, "requires a reason"},
		{"permanent with blank reason", func(m *Mute) { m.Reason = "   " }, "requires a reason"},
		{"both expiry modes", func(m *Mute) { m.ExpiresAt = &future; m.UntilResolved = true }, "not both"},
		{"expiry in the past", func(m *Mute) { m.ExpiresAt = &past }, "in the future"},
		{"missing cluster", func(m *Mute) { m.ClusterID = ""; m.UntilResolved = true }, "clusterId"},
		{"missing rule", func(m *Mute) { m.RuleID = ""; m.UntilResolved = true }, "clusterId"},
		{"missing resource", func(m *Mute) { m.Resource = ""; m.UntilResolved = true }, "clusterId"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base
			tc.mutate(&m)
			err := ValidateMute(m)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("valid mute rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}
