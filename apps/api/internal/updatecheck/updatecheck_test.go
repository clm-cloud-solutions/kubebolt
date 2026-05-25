package updatecheck

import (
	"context"
	"testing"
)

func TestPickHighestStable(t *testing.T) {
	releases := []Release{
		{TagName: "v1.13.0-rc.1", Prerelease: true},
		{TagName: "v1.12.0"},
		{TagName: "v1.12.1"},
		{TagName: "v1.10.5", Draft: true},
		{TagName: "not-a-version"},
	}
	best, err := pickHighestStable(releases)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if best.TagName != "v1.12.1" {
		t.Errorf("expected v1.12.1, got %s", best.TagName)
	}
}

func TestPickHighestStable_NoStable(t *testing.T) {
	releases := []Release{
		{TagName: "v1.13.0-rc.1", Prerelease: true},
		{TagName: "v2.0.0-beta", Prerelease: true},
	}
	if _, err := pickHighestStable(releases); err == nil {
		t.Fatal("expected error when no stable releases present")
	}
}

func TestPickHighestStable_PrefersHighestNotFirst(t *testing.T) {
	// GitHub returns releases in chronological order, not semver order —
	// confirm the highest version wins regardless of position.
	releases := []Release{
		{TagName: "v1.12.0"},
		{TagName: "v1.11.5"},
		{TagName: "v1.13.0"},
		{TagName: "v1.12.1"},
	}
	best, err := pickHighestStable(releases)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if best.TagName != "v1.13.0" {
		t.Errorf("expected v1.13.0, got %s", best.TagName)
	}
}

func TestIsUpdateAvailable(t *testing.T) {
	cases := []struct {
		current string
		latest  string
		want    bool
	}{
		{"v1.12.0", "v1.12.1", true},
		{"v1.12.1", "v1.12.0", false},
		{"v1.12.1", "v1.12.1", false},
		{"1.12.0", "1.13.0", true},
		{"v1.12.0-2-gabc1234", "v1.12.0", false}, // dirty current == base, no update
		{"v1.12.0-2-gabc1234", "v1.12.1", true},
		{"v1.12.0", "invalid", false},
		{"invalid", "v1.12.0", false},
	}
	for _, tc := range cases {
		s := New(tc.current, "", 0)
		if got := s.IsUpdateAvailable(tc.latest); got != tc.want {
			t.Errorf("current=%q latest=%q: got %v want %v",
				tc.current, tc.latest, got, tc.want)
		}
	}
}

func TestIsDevBuild(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"dev", true},
		{"", true},
		{"dev-abc1234", true},
		{"v1.12.0", false},
		{"1.12.0", false},
	}
	for _, tc := range cases {
		s := New(tc.v, "", 0)
		if got := s.IsDevBuild(); got != tc.want {
			t.Errorf("v=%q: got %v want %v", tc.v, got, tc.want)
		}
	}
}

func TestLatest_DevBuildShortCircuits(t *testing.T) {
	// Dev builds must never make a GitHub request — confirm by passing
	// a Service with no httpClient sentinel and checking it returns nil.
	s := New("dev", "", 0)
	rel, err := s.Latest(context.Background())
	if err != nil {
		t.Errorf("dev build must not error: %v", err)
	}
	if rel != nil {
		t.Errorf("dev build must not return a release, got %+v", rel)
	}
}

func TestParseSemver_GitDescribeShapes(t *testing.T) {
	cases := []struct {
		in    string
		ok    bool
		major uint64
		minor uint64
		patch uint64
	}{
		{"v1.12.0", true, 1, 12, 0},
		{"1.12.0", true, 1, 12, 0},
		{"v1.12.0-2-gabc1234", true, 1, 12, 0},
		{"v1.12.0-dirty", true, 1, 12, 0},
		{"v1.13.0-rc.1", true, 1, 13, 0},
		{"dev", false, 0, 0, 0},
		{"", false, 0, 0, 0},
	}
	for _, tc := range cases {
		v, err := parseSemver(tc.in)
		if tc.ok && err != nil {
			t.Errorf("parseSemver(%q) unexpected err: %v", tc.in, err)
			continue
		}
		if !tc.ok && err == nil {
			t.Errorf("parseSemver(%q) expected error", tc.in)
			continue
		}
		if !tc.ok {
			continue
		}
		if v.Major != tc.major || v.Minor != tc.minor || v.Patch != tc.patch {
			t.Errorf("parseSemver(%q) = %d.%d.%d, want %d.%d.%d",
				tc.in, v.Major, v.Minor, v.Patch, tc.major, tc.minor, tc.patch)
		}
	}
}
