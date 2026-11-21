package agentversion

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"golang.org/x/mod/semver"
)

func TestEvaluate(t *testing.T) {
	cases := []struct {
		name     string
		versions []string
		version  string
		level    string
		reasons  int
	}{
		{"current", []string{Latest}, Latest, LevelCurrent, 0},
		{"oldest live agent decides", []string{Latest, "1.4.8"}, "1.4.8", LevelRecommended, 1},
		{"before both fixes", []string{"1.3.1"}, "1.3.1", LevelRecommended, 2},
		{"a leading v is accepted", []string{"v1.4.7"}, "1.4.7", LevelRecommended, 2},
		{"dev builds say nothing", []string{"dev", ""}, "", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Evaluate(c.versions)
			if c.level == "" {
				if got != nil {
					t.Fatalf("Evaluate(%v) = %+v, want nil", c.versions, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("Evaluate(%v) = nil", c.versions)
			}
			if got.Version != c.version || got.Level != c.level || len(got.Reasons) != c.reasons || got.Latest != Latest {
				t.Errorf("Evaluate(%v) = %+v, want version %s level %s with %d reasons", c.versions, got, c.version, c.level, c.reasons)
			}
		})
	}
}

// Behind Latest with no advisory below it reads «update available», not
// «recommended»: a reason to upgrade is a fact about the version, not its age.
func TestEvaluate_AvailableWithoutAdvisory(t *testing.T) {
	saved := Advisories
	t.Cleanup(func() { Advisories = saved })
	Advisories = nil
	got := Evaluate([]string{"1.4.0"})
	if got == nil || got.Level != LevelAvailable || len(got.Reasons) != 0 {
		t.Fatalf("Evaluate(1.4.0) with no advisories = %+v, want available", got)
	}
}

// Every advisory names a released version at or below Latest, oldest first.
func TestAdvisoriesAreOrderedAndReleased(t *testing.T) {
	prev := ""
	for _, a := range Advisories {
		c := canonical(a.Below)
		if c == "" || a.Reason == "" {
			t.Fatalf("advisory %+v: invalid version or empty reason", a)
		}
		if semver.Compare(c, canonical(Latest)) > 0 {
			t.Errorf("advisory below %s is newer than Latest %s", a.Below, Latest)
		}
		if prev != "" && semver.Compare(c, prev) <= 0 {
			t.Errorf("advisories out of order at %s", a.Below)
		}
		prev = c
	}
}

// Latest follows the agent chart: an agent release bumps the chart's
// appVersion, and this fails until Latest is bumped with it.
func TestLatestMatchesTheAgentChart(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "deploy", "helm", "kubebolt-agent", "Chart.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the agent chart: %v", err)
	}
	m := regexp.MustCompile(`(?m)^appVersion:\s*"?([^"\s]+)"?`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("no appVersion in the agent chart")
	}
	if string(m[1]) != Latest {
		t.Errorf("agentversion.Latest = %s, the agent chart's appVersion is %s: bump Latest with the agent release", Latest, m[1])
	}
}

// Agents before 1.4.4 all report 1.0.0 (a constant in the agent until
// 2026-10-12): the version is unknown, not 1.0.0, and the label says what is
// known. Seen live: a 1.3.1 agent shown as «Agent 1.0.0».
func TestEvaluate_UnreportedVersion(t *testing.T) {
	got := Evaluate([]string{"1.0.0", Latest})
	if got == nil || got.Version != "" || got.Before != "1.4.4" || got.Level != LevelRecommended || len(got.Reasons) != len(Advisories) {
		t.Fatalf("Evaluate(1.0.0) = %+v, want an unknown version before 1.4.4, recommended with every reason", got)
	}
	if got.Label() != "before 1.4.4" {
		t.Errorf("Label() = %q, want %q", got.Label(), "before 1.4.4")
	}
	if st := Evaluate([]string{"1.4.8"}); st.Label() != "1.4.8" || st.Before != "" {
		t.Errorf("a reported version labels as itself: %+v", st)
	}
}

// The command to run comes with every status but «current».
func TestEvaluate_UpgradeCommand(t *testing.T) {
	if st := Evaluate([]string{Latest}); st.Upgrade != "" {
		t.Errorf("current agent carries an upgrade command: %q", st.Upgrade)
	}
	st := Evaluate([]string{"1.3.1"})
	want := "helm upgrade kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \\\n  --namespace kubebolt-system --version " + Latest + " --reset-then-reuse-values"
	if st.Upgrade != want {
		t.Errorf("Upgrade = %q, want %q", st.Upgrade, want)
	}
}
