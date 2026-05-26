package promread

import (
	"testing"
	"time"
)

func TestLoadConfigFromEnv_AllUnsetReturnsDisabled(t *testing.T) {
	clearPromreadEnv(t)
	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Enabled {
		t.Errorf("expected Enabled=false when env unset")
	}
	if len(cfg.Matchers) != 0 {
		t.Errorf("expected no matchers when disabled, got %d", len(cfg.Matchers))
	}
}

func TestLoadConfigFromEnv_HappyPath(t *testing.T) {
	clearPromreadEnv(t)
	t.Setenv(envPrefix+"ENABLED", "true")
	t.Setenv(envPrefix+"URL", "http://prom.svc:9090")
	t.Setenv(envPrefix+"AUTH_MODE", "bearer")
	t.Setenv(envPrefix+"BEARER_TOKEN", "deadbeef")
	t.Setenv(envPrefix+"POLL_INTERVAL", "45s")
	t.Setenv(envPrefix+"STEP", "1m")
	t.Setenv(envPrefix+"LOOKBACK", "2m")
	t.Setenv(envPrefix+"MATCHERS", "{__name__=\"up\"}\n{__name__=\"foo\"}\n")

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !cfg.Enabled {
		t.Error("Enabled: want true")
	}
	if cfg.URL != "http://prom.svc:9090" {
		t.Errorf("URL: got %q", cfg.URL)
	}
	if cfg.Auth.Mode != AuthBearer || cfg.Auth.BearerToken != "deadbeef" {
		t.Errorf("Auth: got %+v", cfg.Auth)
	}
	if cfg.PollInterval != 45*time.Second || cfg.Step != time.Minute || cfg.Lookback != 2*time.Minute {
		t.Errorf("durations: %v/%v/%v", cfg.PollInterval, cfg.Step, cfg.Lookback)
	}
	if len(cfg.Matchers) != 2 || cfg.Matchers[0] != `{__name__="up"}` || cfg.Matchers[1] != `{__name__="foo"}` {
		t.Errorf("matchers: %+v", cfg.Matchers)
	}
}

func TestLoadConfigFromEnv_DefaultMatchersWhenEnabledAndMatchersEmpty(t *testing.T) {
	clearPromreadEnv(t)
	t.Setenv(envPrefix+"ENABLED", "true")
	t.Setenv(envPrefix+"URL", "http://prom.svc:9090")

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(cfg.Matchers) != len(DefaultMatchers) {
		t.Errorf("expected default matchers (%d), got %d", len(DefaultMatchers), len(cfg.Matchers))
	}
	// Defensive: confirm we cloned DefaultMatchers rather than aliasing
	// the package-level slice (mutating cfg.Matchers must not poison
	// future loads).
	cfg.Matchers[0] = "MUTATED"
	if DefaultMatchers[0] == "MUTATED" {
		t.Error("Matchers aliased DefaultMatchers — must be a copy")
	}
}

func TestLoadConfigFromEnv_NoDefaultsWhenDisabled(t *testing.T) {
	clearPromreadEnv(t)
	// Enabled stays false; matcher defaults must NOT be injected to
	// avoid surprising operators who explicitly left promread off.
	cfg, _ := LoadConfigFromEnv()
	if len(cfg.Matchers) != 0 {
		t.Errorf("expected no defaults when disabled, got %d matchers", len(cfg.Matchers))
	}
}

func TestLoadConfigFromEnv_BadBoolRejected(t *testing.T) {
	clearPromreadEnv(t)
	t.Setenv(envPrefix+"ENABLED", "yesplease")
	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("expected error for unparseable bool")
	}
}

func TestLoadConfigFromEnv_BadDurationRejected(t *testing.T) {
	clearPromreadEnv(t)
	t.Setenv(envPrefix+"ENABLED", "true")
	t.Setenv(envPrefix+"URL", "http://x")
	t.Setenv(envPrefix+"POLL_INTERVAL", "an hour")
	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("expected error for unparseable duration")
	}
}

func TestParseMatchers_TrimsAndDropsEmpties(t *testing.T) {
	got := parseMatchers("  {a=\"1\"}  \n\n  {b=\"2\"}  \n   \n")
	if len(got) != 2 || got[0] != `{a="1"}` || got[1] != `{b="2"}` {
		t.Errorf("parseMatchers: %+v", got)
	}
}

// clearPromreadEnv wipes every KUBEBOLT_AGENT_PROMREAD_* key the
// loader inspects. t.Setenv handles per-test isolation when we set
// a key, but defaults from the operator's shell can still bleed in
// when we DON'T set one and the var is present in the environment
// running the tests (e.g. an integration runner). Explicit unset
// keeps the tests deterministic.
func clearPromreadEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"ENABLED",
		"URL",
		"AUTH_MODE",
		"BASIC_AUTH_USERNAME",
		"BASIC_AUTH_PASSWORD",
		"BEARER_TOKEN",
		"POLL_INTERVAL",
		"STEP",
		"LOOKBACK",
		"MATCHERS",
	} {
		t.Setenv(envPrefix+k, "")
	}
}
