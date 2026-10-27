// Package fieldmanager names KubeBolt in the clusters it writes to.
//
// Kubernetes records who wrote each field of an object (metadata.managedFields)
// under the field manager the write carries: kubectl writes as kubectl-rollout
// or kubectl-client-side-apply, Argo CD and Helm under their own names. A write
// that names none is recorded under the client's binary name, and KubeBolt's
// came out as "server" (db-role-password-rotated, 2026-10-12): Kobi, reading
// who restarted a Deployment, could not tell it was KubeBolt. Every create,
// update and patch KubeBolt sends names Name; fieldmanager_test fails the build
// on one that does not.
package fieldmanager

// Name is the field manager on every KubeBolt write. Who did it, inside
// KubeBolt, is in the audit trail.
const Name = "kubebolt"
