package mcpcatalog

import (
	"strings"
	"testing"
)

func minPlanTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	mode := AuthMode{ID: "key", Label: "Key"}
	c, err := NewWith(
		&ConnectorDef{ID: "alpha", Name: "Alpha", MinPlan: "team", AuthModes: []AuthMode{mode}, Binder: nopBinder{}},
		&ConnectorDef{ID: "beta", Name: "Beta", MinPlan: "team", AuthModes: []AuthMode{mode}, Binder: nopBinder{}},
	)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	return c
}

// The override replaces the named connector's minimum in a NEW catalog and
// reports it as coming from the environment; the others keep their code
// default, and so does the catalog the override was applied to.
func TestWithMinPlans_OverridesOnlyTheNamedConnectors(t *testing.T) {
	base := minPlanTestCatalog(t)
	eff, err := base.WithMinPlans(map[string]string{"beta": "business"})
	if err != nil {
		t.Fatalf("WithMinPlans: %v", err)
	}

	beta, _ := eff.Get("beta")
	if beta.MinPlan != "business" {
		t.Errorf("effective beta minPlan = %q, want business", beta.MinPlan)
	}
	if got := eff.MinPlanSource("beta"); got != MinPlanSourceEnv {
		t.Errorf("beta source = %q, want %q", got, MinPlanSourceEnv)
	}
	alpha, _ := eff.Get("alpha")
	if alpha.MinPlan != "team" || eff.MinPlanSource("alpha") != MinPlanSourceDefault {
		t.Errorf("alpha = (%q, %q), want (team, %q)", alpha.MinPlan, eff.MinPlanSource("alpha"), MinPlanSourceDefault)
	}

	// List and Get agree on the overridden definition, in the same order.
	var ids []string
	for _, d := range eff.List() {
		ids = append(ids, d.ID)
		if g, _ := eff.Get(d.ID); g != d {
			t.Errorf("List and Get disagree on %q", d.ID)
		}
	}
	if strings.Join(ids, ",") != "alpha,beta" {
		t.Errorf("order = %v, want [alpha beta]", ids)
	}

	// The base catalog and its definitions are untouched: whoever built on
	// them keeps the price they had.
	orig, _ := base.Get("beta")
	if orig.MinPlan != "team" || base.MinPlanSource("beta") != MinPlanSourceDefault {
		t.Errorf("base beta = (%q, %q), want (team, default): the override leaked into the original", orig.MinPlan, base.MinPlanSource("beta"))
	}
	if orig == beta {
		t.Error("the overridden definition is the original pointer, not a copy")
	}
	// Everything but the minimum is the same connector.
	if beta.Binder != orig.Binder || beta.Name != orig.Name || len(beta.AuthModes) != len(orig.AuthModes) {
		t.Error("the copy lost part of the definition")
	}
}

// An id the catalog does not ship is refused, naming the ids it does.
func TestWithMinPlans_RefusesAnUnknownConnector(t *testing.T) {
	_, err := minPlanTestCatalog(t).WithMinPlans(map[string]string{"alpha": "team", "gamma": "business"})
	if err == nil {
		t.Fatal("an unknown connector id was accepted")
	}
	for _, want := range []string{`"gamma"`, "alpha, beta"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

// No override is the shipped catalog: every source is the default.
func TestWithMinPlans_EmptyKeepsTheDefaults(t *testing.T) {
	eff, err := minPlanTestCatalog(t).WithMinPlans(nil)
	if err != nil {
		t.Fatalf("WithMinPlans(nil): %v", err)
	}
	for _, d := range eff.List() {
		if d.MinPlan != "team" || eff.MinPlanSource(d.ID) != MinPlanSourceDefault {
			t.Errorf("%s = (%q, %q), want (team, default)", d.ID, d.MinPlan, eff.MinPlanSource(d.ID))
		}
	}
	if got := eff.MinPlanSource("nope"); got != MinPlanSourceDefault {
		t.Errorf("unknown id source = %q, want %q", got, MinPlanSourceDefault)
	}
}

// Applying a second override keeps the first one's source.
func TestWithMinPlans_Chains(t *testing.T) {
	one, err := minPlanTestCatalog(t).WithMinPlans(map[string]string{"alpha": "business"})
	if err != nil {
		t.Fatal(err)
	}
	two, err := one.WithMinPlans(map[string]string{"beta": "enterprise"})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"alpha": "business", "beta": "enterprise"} {
		d, _ := two.Get(id)
		if d.MinPlan != want || two.MinPlanSource(id) != MinPlanSourceEnv {
			t.Errorf("%s = (%q, %q), want (%q, env)", id, d.MinPlan, two.MinPlanSource(id), want)
		}
	}
}
