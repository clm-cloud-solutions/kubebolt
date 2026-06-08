package auth

// edition.go holds the OSS↔EE capability seam for org/team management.
//
// OSS operates a single, fixed hierarchy: ONE organization (the auto-seeded
// "default" tenant) with ONE team ("default") under it, and every user is a
// member of both. Creating additional tenants or teams is an EE/SaaS feature.
// The line is drawn HERE, server-side, behind a build-tag seam — the OSS
// backend returns 409 + code "requires_ee" so the UI can surface an upgrade
// CTA, while EE flips the switch to unlock the management endpoints.
//
// This replaces the earlier "expose everything, let the frontend hide it"
// policy: a UI-only guard is not a real boundary (the REST surface was still
// reachable). The seam mirrors DefaultTenantResolver (tenant_context.go) — OSS
// ships the default value; EE overrides it from a build-tagged file that lives
// only in the private repo:
//
//	//go:build ee
//	func init() { MultiTenantEnabled = true }
//
// See internal/saas/kubebolt-oss-ee-split-runbook.md §2.1.
var MultiTenantEnabled = false

// ErrCodeRequiresEE is the machine-readable code returned in the JSON error
// body when an OSS install rejects a multi-org / multi-team operation. The
// frontend keys off this (not the human message) to render the upgrade CTA.
const ErrCodeRequiresEE = "requires_ee"
