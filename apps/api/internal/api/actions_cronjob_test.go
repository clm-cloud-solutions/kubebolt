package api

import (
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// CronJob action tests focus on:
//   1. Suspend/resume PATCH SHAPE — exact bytes the apiserver
//      receives. A drifted shape would either fail validation or
//      (worse) be a silent no-op.
//   2. Manual JOB CONSTRUCTION — every field that kubectl's
//      `create job --from=cronjob/X` sets must also be set here:
//      jobTemplate spec verbatim, instantiate=manual annotation,
//      OwnerReference back to the parent, copied labels.
//   3. Response SHAPE for suspend/resume — the {status, alreadyX,
//      cronJob} envelope is what the frontend setQueryData hooks
//      into.

func TestBuildCronJobSuspendPatchTrue(t *testing.T) {
	got, err := buildCronJobSuspendPatch(true)
	if err != nil {
		t.Fatalf("buildCronJobSuspendPatch(true): %v", err)
	}
	want := `{"spec":{"suspend":true}}`
	if string(got) != want {
		t.Errorf("suspend patch = %s, want %s", got, want)
	}
}

func TestBuildCronJobSuspendPatchFalse(t *testing.T) {
	got, err := buildCronJobSuspendPatch(false)
	if err != nil {
		t.Fatalf("buildCronJobSuspendPatch(false): %v", err)
	}
	want := `{"spec":{"suspend":false}}`
	if string(got) != want {
		t.Errorf("resume patch = %s, want %s", got, want)
	}
}

// TestBuildManualJobFromCronJob verifies the constructed Job has
// every property kubectl sets when running
// `create job --from=cronjob/X`. If any of these drift, the manual
// run won't appear in the CronJob's child-jobs list, GC won't work,
// or `kubectl describe job` will lie about the origin.
func TestBuildManualJobFromCronJob(t *testing.T) {
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "daily-backup",
			Namespace: "ops",
			UID:       types.UID("cj-uid-123"),
		},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 3 * * *",
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":  "backup",
						"team": "ops",
					},
				},
				Spec: batchv1.JobSpec{
					BackoffLimit: int32Ptr(2),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyOnFailure,
							Containers: []corev1.Container{{
								Name:  "backup",
								Image: "ghcr.io/acme/backup:v1",
							}},
						},
					},
				},
			},
		},
	}

	job := buildManualJobFromCronJob(cj, "daily-backup-manual-001", "alice")

	if job.Name != "daily-backup-manual-001" {
		t.Errorf("Name = %q, want daily-backup-manual-001", job.Name)
	}
	if job.Namespace != "ops" {
		t.Errorf("Namespace = %q, want ops", job.Namespace)
	}

	// Annotations — the standard kubectl marker + KubeBolt audit.
	if got := job.Annotations["cronjob.kubernetes.io/instantiate"]; got != "manual" {
		t.Errorf("instantiate annotation = %q, want manual (kubectl writes this; missing means describe-job lies about origin)", got)
	}
	if got := job.Annotations["kubebolt.io/triggered-by"]; got != "alice" {
		t.Errorf("triggered-by annotation = %q, want alice", got)
	}
	if got := job.Annotations["kubebolt.io/triggered-from"]; got != "daily-backup" {
		t.Errorf("triggered-from annotation = %q, want daily-backup", got)
	}

	// Labels copied from jobTemplate.Labels — selectors that match
	// scheduled runs must also match manual ones.
	if job.Labels["app"] != "backup" || job.Labels["team"] != "ops" {
		t.Errorf("labels not copied from jobTemplate: %v", job.Labels)
	}

	// OwnerReference — what makes the manual run appear in the
	// CronJob's child-jobs list and propagates GC on parent
	// deletion.
	if len(job.OwnerReferences) != 1 {
		t.Fatalf("OwnerReferences len = %d, want 1", len(job.OwnerReferences))
	}
	owner := job.OwnerReferences[0]
	if owner.Kind != "CronJob" || owner.Name != "daily-backup" || owner.UID != cj.UID {
		t.Errorf("OwnerReference wrong: %+v", owner)
	}
	if owner.APIVersion != "batch/v1" {
		t.Errorf("OwnerReference.APIVersion = %q, want batch/v1", owner.APIVersion)
	}

	// Spec must be a verbatim copy of jobTemplate.Spec — including
	// backoffLimit, the pod template, restart policy. Drift here
	// would mean manual runs behave differently from scheduled
	// runs, defeating the point of the feature.
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 2 {
		t.Errorf("BackoffLimit not preserved: %v", job.Spec.BackoffLimit)
	}
	if job.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyOnFailure {
		t.Errorf("RestartPolicy not preserved: %v", job.Spec.Template.Spec.RestartPolicy)
	}
	if len(job.Spec.Template.Spec.Containers) != 1 ||
		job.Spec.Template.Spec.Containers[0].Image != "ghcr.io/acme/backup:v1" {
		t.Errorf("container spec not preserved: %v", job.Spec.Template.Spec.Containers)
	}
}

func TestBuildManualJobFromCronJobNoUsername(t *testing.T) {
	// When auth is disabled, ContextClaims returns nil and we pass
	// "" for the username. The triggered-by annotation must NOT be
	// set in that case — we shouldn't lie about who ran it.
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "cron", Namespace: "ns"},
		Spec: batchv1.CronJobSpec{
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "c", Image: "img"}},
					}},
				},
			},
		},
	}
	job := buildManualJobFromCronJob(cj, "cron-manual-1", "")

	if _, has := job.Annotations["kubebolt.io/triggered-by"]; has {
		t.Error("triggered-by annotation should be absent when no username — got it set")
	}
	// instantiate annotation must still be set (it's about origin,
	// not authorship).
	if job.Annotations["cronjob.kubernetes.io/instantiate"] != "manual" {
		t.Error("instantiate annotation should always be present")
	}
}

func TestBuildManualJobFromCronJobNilLabels(t *testing.T) {
	// Many CronJobs don't set jobTemplate labels at all. Must not
	// panic and must produce a Job with at least an empty labels
	// map (so downstream code can read it without nil checks).
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "cron", Namespace: "ns"},
		Spec: batchv1.CronJobSpec{
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "c", Image: "img"}},
					}},
				},
			},
		},
	}
	job := buildManualJobFromCronJob(cj, "cron-manual-1", "alice")

	if job.Labels == nil {
		t.Error("Labels = nil, want empty map (or populated)")
	}
}

func TestBuildCronJobSuspendResponseSuspend(t *testing.T) {
	cj := map[string]interface{}{"name": "daily-backup", "suspend": true}

	got := buildCronJobSuspendResponse("suspend", cj, false)
	if got["status"] != "suspended" {
		t.Errorf("status = %v, want suspended", got["status"])
	}
	if got["alreadySuspended"] != false {
		t.Errorf("alreadySuspended = %v, want false", got["alreadySuspended"])
	}
	if _, has := got["alreadyActive"]; has {
		t.Error("response must not include alreadyActive for suspend action")
	}

	got = buildCronJobSuspendResponse("suspend", cj, true)
	if got["alreadySuspended"] != true {
		t.Errorf("alreadySuspended = %v, want true", got["alreadySuspended"])
	}
}

func TestBuildCronJobSuspendResponseResume(t *testing.T) {
	cj := map[string]interface{}{"name": "daily-backup", "suspend": false}

	got := buildCronJobSuspendResponse("resume", cj, false)
	if got["status"] != "resumed" {
		t.Errorf("status = %v, want resumed", got["status"])
	}
	if got["alreadyActive"] != false {
		t.Errorf("alreadyActive = %v, want false", got["alreadyActive"])
	}
	if _, has := got["alreadySuspended"]; has {
		t.Error("response must not include alreadySuspended for resume action")
	}
}

// Helper: int32Ptr is the standard Kubernetes idiom for *int32
// fields (BackoffLimit, Replicas, etc.). Not exported because the
// other test files don't need it; keep local.
func int32Ptr(v int32) *int32 { return &v }
