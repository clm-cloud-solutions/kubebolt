// OSS shim of the Enterprise capabilities section: the Enterprise build
// renders the org's capability registry (#50 — plan caps, credits, Autopilot,
// notifications) in Plan & usage and reuses these labels on the shift-report
// card. OSS has no registry — the report's capability list is always empty —
// but the card stays byte-identical, so the labels it imports live here.
export const LABELS: Record<string, string> = {
  ingest_caps: 'Ingest',
  active_series: 'Metric series',
  credits: 'AI credits',
  autopilot: 'Autopilot',
  notifications: 'Notifications',
  detection: 'Detection',
  subscription: 'Subscription',
}
