package api

import "testing"

// cleanDrainError pins the upstream kubectl stutter fix (in-vivo find
// 31-ago: the drain modal read «cannot delete cannot delete Pods…»).
func TestCleanDrainError(t *testing.T) {
	stuttered := "cannot delete cannot delete Pods that declare no controller (use --force to override): np-test-allany/worker, np-test-ok/web"
	want := "cannot delete Pods that declare no controller (use --force to override): np-test-allany/worker, np-test-ok/web"
	if got := cleanDrainError(stuttered); got != want {
		t.Fatalf("got %q", got)
	}
	// The reasons that compose correctly upstream pass through untouched.
	for _, ok := range []string{
		"cannot delete DaemonSet-managed Pods (use --ignore-daemonsets to ignore): kube-system/kindnet-x",
		"cannot delete Pods with local storage (use --delete-emptydir-data to override): ns/cache",
		"error when evicting pods/\"web-1\": global timeout reached",
	} {
		if got := cleanDrainError(ok); got != ok {
			t.Fatalf("clean mangled a healthy message: %q → %q", ok, got)
		}
	}
}
