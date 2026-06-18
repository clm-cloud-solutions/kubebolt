package auth

// edition.go holds the edition seam for multi-org / multi-tenant management.
//
// Multi-tenant (multiple organizations) is a CLOUD-ONLY capability. There are
// three editions, separated by build tags:
//
//	OSS            (no tags)        → single-tenant
//	EE Self-Hosted (-tags ee)       → single-tenant, FORCED
//	Cloud SaaS     (-tags ee,saas)  → multi-tenant
//
// OSS and EE Self-Hosted operate a single, fixed organization (the auto-seeded
// "default" tenant) with its team(s) under it. Standing up a SECOND organization
// is a Cloud-only feature: the multi-org endpoints (tenant CRUD, self-service
// signup) return 409 + code "requires_ee" when MultiTenantEnabled is false, and
// the flag is flipped ONLY by edition_saas.go (//go:build saas) — never by `ee`
// alone. That makes single-tenant a hard, build-level guarantee for OSS and EE
// Self-Hosted: no env var or runtime switch can create a second tenant; only a
// fork + rebuild could. The `saas` tag is meaningful only with `ee`, which
// provides the Postgres stores multi-tenancy relies on.
//
// See internal/saas/kubebolt-oss-ee-split-runbook.md §2.1.
var MultiTenantEnabled = false

// ErrCodeRequiresEE is the machine-readable code returned in the JSON error
// body when an OSS install rejects a multi-org / multi-team operation. The
// frontend keys off this (not the human message) to render the upgrade CTA.
const ErrCodeRequiresEE = "requires_ee"
