import { PerTenantLimitsSection } from './PerTenantLimitsSection'

// IngestSettingsTab is a thin shell around the per-tenant overrides UI.
//
// Spec #09 initially proposed a separate "fleet defaults" form here
// distinct from per-tenant overrides. In single-tenant OSS the two
// collapse to the same operational concern: the default tenant's limits
// ARE the cluster's effective limits. Surfacing both editors would
// duplicate the same action with two different mental models, so the
// fleet-defaults form was removed and only the migrated per-tenant
// section remains (the design with source badges, dirty markers, and
// per-field "Default" hints from the retired /admin/ingest-limits page).
//
// When multi-tenant management lands (Enterprise), a sibling card for
// "fleet defaults across all tenants" can re-emerge here — by then the
// distinction will be operationally meaningful.

export function IngestSettingsTab() {
  return <PerTenantLimitsSection />
}
