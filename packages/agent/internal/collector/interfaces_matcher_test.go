package collector

import "testing"

func TestInterfaceMatcher_EmptyDropsNothing(t *testing.T) {
	m, err := NewInterfaceMatcher(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Drops("eth0") || m.Drops("") {
		t.Fatal("empty matcher must keep every interface")
	}
	var nilM *InterfaceMatcher
	if nilM.Drops("eth0") {
		t.Fatal("nil matcher must keep every interface")
	}
	if nilM.Size() != 0 {
		t.Fatal("nil matcher size must be 0")
	}
}

// Exact-only entries behave exactly like the pre-prefix droplist
// (regression guard for existing operator configs).
func TestInterfaceMatcher_ExactParity(t *testing.T) {
	m, err := NewInterfaceMatcher([]string{"sit0", "gre0", " tunl0 "})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, name := range []string{"sit0", "gre0", "tunl0"} {
		if !m.Drops(name) {
			t.Errorf("exact entry %q must drop", name)
		}
	}
	for _, name := range []string{"sit", "sit01", "eth0", ""} {
		if m.Drops(name) {
			t.Errorf("exact matcher must not drop %q", name)
		}
	}
	if m.Size() != 3 {
		t.Errorf("Size() = %d, want 3", m.Size())
	}
}

func TestInterfaceMatcher_PrefixOnly(t *testing.T) {
	m, err := NewInterfaceMatcher([]string{"azv*"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Matches the hash-suffixed names AND the bare prefix (empty suffix).
	for _, name := range []string{"azv1234", "azvff00aa", "azv"} {
		if !m.Drops(name) {
			t.Errorf("prefix azv* must drop %q", name)
		}
	}
	// Prefix means PREFIX — not substring.
	for _, name := range []string{"xazv1234", "az", "eth0"} {
		if m.Drops(name) {
			t.Errorf("prefix azv* must not drop %q", name)
		}
	}
}

func TestInterfaceMatcher_Mixed(t *testing.T) {
	m, err := NewInterfaceMatcher([]string{"lo", "veth*", "cali*", "sit0"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cases := map[string]bool{
		"lo":        true,
		"veth0abc":  true,
		"cali12de":  true,
		"sit0":      true,
		"eth0":      false,
		"cilium_ho": false,
		"enP1s0":    false,
	}
	for name, want := range cases {
		if got := m.Drops(name); got != want {
			t.Errorf("Drops(%q) = %v, want %v", name, got, want)
		}
	}
	if m.Size() != 4 {
		t.Errorf("Size() = %d, want 4", m.Size())
	}
}

// A bare "*" would drop every interface — a footgun that must fail
// loudly at parse time, not silently blind the network panels.
func TestInterfaceMatcher_RejectsBareStar(t *testing.T) {
	if _, err := NewInterfaceMatcher([]string{"*"}); err == nil {
		t.Fatal("bare * must be rejected")
	}
}

// "*" anywhere but the trailing position is unsupported (full regex is
// a separate proposal) — reject rather than mis-match.
func TestInterfaceMatcher_RejectsInnerStar(t *testing.T) {
	for _, e := range []string{"foo*bar", "*eth0", "a*b*"} {
		if _, err := NewInterfaceMatcher([]string{e}); err == nil {
			t.Errorf("entry %q must be rejected", e)
		}
	}
}
