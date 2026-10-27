package cluster

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
)

func TestDeploymentStatus(t *testing.T) {
	deploy := func(desired *int32, existing, available int32) *appsv1.Deployment {
		return &appsv1.Deployment{
			Spec:   appsv1.DeploymentSpec{Replicas: desired},
			Status: appsv1.DeploymentStatus{Replicas: existing, AvailableReplicas: available},
		}
	}
	n := func(v int32) *int32 { return &v }

	cases := []struct {
		name string
		d    *appsv1.Deployment
		want string
	}{
		// The live test of 2026-10-10: oomy-app scaled to 0 read "Running",
		// and Kobi had to explain the label away.
		{"scaled to 0", deploy(n(0), 0, 0), "Scaled to 0"},
		{"scaled to 0, pod not gone yet", deploy(n(0), 1, 1), "Scaled to 0"},
		{"all available", deploy(n(3), 3, 3), "Running"},
		// A ResourceQuota let 1 of 3 pods be created: everything that exists
		// is available, and it used to read "Running".
		{"quota refused the rest", deploy(n(3), 1, 1), "Warning"},
		{"quota refused every pod", deploy(n(3), 0, 0), "Error"},
		{"none available", deploy(n(2), 2, 0), "Error"},
		// Rolling update: the surge pod exists and is not available yet.
		{"surge pod starting", deploy(n(3), 4, 3), "Warning"},
		{"no replicas field means 1", deploy(nil, 1, 1), "Running"},
	}
	for _, c := range cases {
		if got := deploymentStatus(c.d); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
