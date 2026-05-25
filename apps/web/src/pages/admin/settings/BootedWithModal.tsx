import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, Eye, EyeOff, Info, Pencil } from 'lucide-react'
import { api } from '@/services/api'
import { Modal } from '@/components/shared/Modal'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'

// BootedWithModal — read-only diagnostic view answering "which Helm /
// env values did this container actually boot with?". Surfaced from
// the Settings page header.
//
// Spec #09 V2 extends the V1 flat table into a category-grouped view:
//
//   [BOOT-TIME] Process bootstrap     — logger, data dir, bind ports
//   [BOOT-TIME] Auth bootstrap        — JWT secret, admin password
//   [BOOT-TIME] Filesystem paths      — TLS cert/key paths
//   [BOOT-TIME] Infra integration     — VictoriaMetrics URL, retention
//   [Runtime]   Editable via Settings — every Bucket B var, plus V1 domains
//
// Each runtime-configurable row also surfaces which Settings tab edits
// it, so operators can answer "where do I change this?" without
// re-discovering the tab structure.
//
// Sensitive entries (JWT secret, API keys, webhook URLs, SMTP password)
// continue to show "(set, redacted)" rather than the cleartext.

type Bucket = 'process' | 'auth-boot' | 'filesystem' | 'infra' | 'runtime'
type SettingsTab = 'general' | 'copilot' | 'notifications' | 'auth' | 'ingest'

interface Categorization {
  bucket: Bucket
  // null when env-only (Bucket A). When set, the operator can edit
  // this var via the named tab; the modal renders a small badge.
  configuredVia: { tab: SettingsTab; label: string } | null
}

// Hard-coded match table. Order: exact match first; the prefix rule
// at the bottom handles families like KUBEBOLT_AI_* / KUBEBOLT_SMTP_*
// without listing every member. New env vars added later either land
// in 'runtime' (if covered by a Settings tab) or fall through to the
// 'process' default (which renders as boot-time uncategorized — a
// signal to update this map).
const EXACT_MATCHES: Record<string, Categorization> = {
  // ─── A1 Process bootstrap ────────────────────────────────────────
  KUBEBOLT_LOG_LEVEL: { bucket: 'process', configuredVia: null },
  KUBEBOLT_LOG_FORMAT: { bucket: 'process', configuredVia: null },
  KUBEBOLT_LOG_DIR: { bucket: 'process', configuredVia: null },
  KUBEBOLT_DATA_DIR: { bucket: 'process', configuredVia: null },
  KUBEBOLT_API_PORT: { bucket: 'process', configuredVia: null },
  KUBEBOLT_AGENT_GRPC_ADDR: { bucket: 'process', configuredVia: null },
  KUBEBOLT_AI_DEBUG: { bucket: 'process', configuredVia: null }, // legacy

  // ─── A2 Auth bootstrap ───────────────────────────────────────────
  KUBEBOLT_AUTH_ENABLED: { bucket: 'auth-boot', configuredVia: null },
  KUBEBOLT_JWT_SECRET: { bucket: 'auth-boot', configuredVia: null },
  KUBEBOLT_ADMIN_PASSWORD: { bucket: 'auth-boot', configuredVia: null },
  KUBEBOLT_AUTH_INITIAL_ADMIN_PASSWORD: { bucket: 'auth-boot', configuredVia: null },
  KUBEBOLT_RESET_ADMIN_PASSWORD: { bucket: 'auth-boot', configuredVia: null },

  // ─── A3 Filesystem paths ─────────────────────────────────────────
  KUBEBOLT_AGENT_TLS_CERT_FILE: { bucket: 'filesystem', configuredVia: null },
  KUBEBOLT_AGENT_TLS_KEY_FILE: { bucket: 'filesystem', configuredVia: null },
  KUBEBOLT_AGENT_TLS_CLIENT_CA: { bucket: 'filesystem', configuredVia: null },

  // ─── A4 Infra integration ────────────────────────────────────────
  KUBEBOLT_METRICS_STORAGE_URL: { bucket: 'infra', configuredVia: null },
  KUBEBOLT_METRICS_RETENTION: { bucket: 'infra', configuredVia: null },

  // ─── Runtime — Settings → Auth (V1) ──────────────────────────────
  KUBEBOLT_JWT_EXPIRY: { bucket: 'runtime', configuredVia: { tab: 'auth', label: 'Settings → Auth' } },
  KUBEBOLT_JWT_REFRESH_EXPIRY: { bucket: 'runtime', configuredVia: { tab: 'auth', label: 'Settings → Auth' } },

  // ─── Runtime — Settings → General (V1 + V2) ──────────────────────
  KUBEBOLT_DISPLAY_NAME: { bucket: 'runtime', configuredVia: { tab: 'general', label: 'Settings → General' } },
  KUBEBOLT_DEFAULT_REFRESH_INTERVAL_SECONDS: { bucket: 'runtime', configuredVia: { tab: 'general', label: 'Settings → General' } },
  KUBEBOLT_PROD_NAMESPACE_PATTERN: { bucket: 'runtime', configuredVia: { tab: 'general', label: 'Settings → General' } },

  // ─── Runtime — Settings → Agents & Ingest (V2) ──────────────────
  KUBEBOLT_AGENT_AUTH_MODE: { bucket: 'runtime', configuredVia: { tab: 'ingest', label: 'Settings → Agents & Ingest' } },
  KUBEBOLT_AGENT_TOKEN_AUDIENCE: { bucket: 'runtime', configuredVia: { tab: 'ingest', label: 'Settings → Agents & Ingest' } },
  KUBEBOLT_AGENT_REQUIRE_MTLS: { bucket: 'runtime', configuredVia: { tab: 'ingest', label: 'Settings → Agents & Ingest' } },
  KUBEBOLT_AGENT_RATE_LIMIT_ENABLED: { bucket: 'runtime', configuredVia: { tab: 'ingest', label: 'Settings → Agents & Ingest' } },
  KUBEBOLT_AGENT_RATE_LIMIT_RPS: { bucket: 'runtime', configuredVia: { tab: 'ingest', label: 'Settings → Agents & Ingest' } },
  KUBEBOLT_AGENT_RATE_LIMIT_BURST: { bucket: 'runtime', configuredVia: { tab: 'ingest', label: 'Settings → Agents & Ingest' } },
  KUBEBOLT_AGENT_AUTOREGISTER_CLUSTERS: { bucket: 'runtime', configuredVia: { tab: 'ingest', label: 'Settings → Agents & Ingest' } },
  KUBEBOLT_AGENT_REGISTRY_PRUNE_HORIZON: { bucket: 'runtime', configuredVia: { tab: 'ingest', label: 'Settings → Agents & Ingest' } },
  KUBEBOLT_AGENT_TUNNEL_IDLE_TIMEOUT: { bucket: 'runtime', configuredVia: { tab: 'ingest', label: 'Settings → Agents & Ingest' } },
  KUBEBOLT_REMOTE_WRITE_ENABLED: { bucket: 'runtime', configuredVia: { tab: 'ingest', label: 'Settings → Agents & Ingest' } },
  KUBEBOLT_REMOTE_WRITE_AUTH_MODE: { bucket: 'runtime', configuredVia: { tab: 'ingest', label: 'Settings → Agents & Ingest' } },
  KUBEBOLT_PROM_WRITE_DEFAULT_SAMPLES_PER_SEC: { bucket: 'runtime', configuredVia: { tab: 'ingest', label: 'Settings → Agents & Ingest' } },
  KUBEBOLT_PROM_WRITE_DEFAULT_BURST_SAMPLES: { bucket: 'runtime', configuredVia: { tab: 'ingest', label: 'Settings → Agents & Ingest' } },
  KUBEBOLT_PROM_WRITE_DEFAULT_MAX_ACTIVE_SERIES: { bucket: 'runtime', configuredVia: { tab: 'ingest', label: 'Settings → Agents & Ingest' } },
  KUBEBOLT_PROM_WRITE_DEFAULT_MAX_ACTIVE_SERIES_GLOBAL: { bucket: 'runtime', configuredVia: { tab: 'ingest', label: 'Settings → Agents & Ingest' } },
}

function categorize(name: string): Categorization {
  if (name in EXACT_MATCHES) return EXACT_MATCHES[name]
  // Family prefixes — covers expanding sets without map churn.
  if (name.startsWith('KUBEBOLT_AI_')) {
    return { bucket: 'runtime', configuredVia: { tab: 'copilot', label: 'Settings → AI Copilot' } }
  }
  if (name.startsWith('KUBEBOLT_SLACK_') || name.startsWith('KUBEBOLT_DISCORD_') || name.startsWith('KUBEBOLT_SMTP_') || name.startsWith('KUBEBOLT_NOTIFICATIONS_')) {
    return { bucket: 'runtime', configuredVia: { tab: 'notifications', label: 'Settings → Notifications' } }
  }
  // Unknown KUBEBOLT_* — treat as boot-time uncategorized so it
  // surfaces under "Process bootstrap" with an Info icon. Operators
  // who see one of these here should ping the team to add it to the
  // map above (or just leave it — the table still renders correctly).
  return { bucket: 'process', configuredVia: null }
}

const BUCKET_INFO: Record<Bucket, { title: string; subtitle: string; tag: 'BOOT-TIME' | 'RUNTIME' }> = {
  process: {
    title: 'Process bootstrap',
    subtitle: 'Read before the logger / BoltDB are initialized — must be env-only.',
    tag: 'BOOT-TIME',
  },
  'auth-boot': {
    title: 'Auth bootstrap',
    subtitle: 'JWT signing key, admin seeding, recovery escape hatches. Editing via UI would invalidate sessions or lock you out.',
    tag: 'BOOT-TIME',
  },
  filesystem: {
    title: 'Filesystem paths',
    subtitle: 'Cert / key files loaded into the TLS listener at boot. UI upload flow lives in a future spec.',
    tag: 'BOOT-TIME',
  },
  infra: {
    title: 'Infra integration',
    subtitle: 'Endpoints discovered at startup. Editing via UI would break in-flight queries against the previous target.',
    tag: 'BOOT-TIME',
  },
  runtime: {
    title: 'Runtime-configurable',
    subtitle: 'Editable from a Settings tab without redeploy. The env value is the boot fallback; UI overrides win.',
    tag: 'RUNTIME',
  },
}

// Order the buckets render in. Boot-time groups first (top-of-page
// "what's pinned at startup"), runtime last ("what you can change
// right now"). Within each group, env names are sorted A-Z (server
// already does this).
const BUCKET_ORDER: Bucket[] = ['process', 'auth-boot', 'filesystem', 'infra', 'runtime']

export function BootedWithModal({ onClose }: { onClose: () => void }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ['admin', 'settings', 'booted-with'],
    queryFn: api.getBootedWith,
    // Boot snapshot doesn't change for the lifetime of the process —
    // cache aggressively, no refetch on focus.
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  })

  // Group entries by bucket. Each entry carries its categorization
  // result so the runtime group can render the per-row "configured via"
  // badge without re-running the match.
  const groups: Record<Bucket, Array<{
    name: string
    value: string
    sensitive: boolean
    configuredVia: Categorization['configuredVia']
  }>> | null = (() => {
    if (!data) return null
    const out: Record<Bucket, Array<{
      name: string
      value: string
      sensitive: boolean
      configuredVia: Categorization['configuredVia']
    }>> = { process: [], 'auth-boot': [], filesystem: [], infra: [], runtime: [] }
    for (const e of data.env) {
      const cat = categorize(e.name)
      out[cat.bucket].push({
        name: e.name,
        value: e.value,
        sensitive: e.sensitive,
        configuredVia: cat.configuredVia,
      })
    }
    return out
  })()

  return (
    <Modal badge="Boot" title="KUBEBOLT_* environment at boot" onClose={onClose} size="lg">
      <div className="flex-1 overflow-y-auto p-5 space-y-3">
        <p className="text-[11px] text-kb-text-tertiary leading-relaxed">
          Every <code className="font-mono text-kb-accent">KUBEBOLT_*</code> environment variable
          the process saw at start, grouped by whether it can be changed at runtime via Settings
          (lower section) or stays pinned to its boot value (upper sections). Settings overrides
          take precedence over env on every read; the env value here is the boot fallback.
        </p>

        {isLoading && (
          <div className="py-10 flex justify-center">
            <LoadingSpinner size="md" />
          </div>
        )}

        {error && (
          <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">
            <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
            <div>{(error as Error)?.message || 'Failed to read boot snapshot'}</div>
          </div>
        )}

        {data && data.env.length === 0 && (
          <div className="text-xs text-kb-text-tertiary italic">
            No KUBEBOLT_* env vars were set at boot. The process is running with full
            defaults — every Settings tab shows the baseline as if no Helm chart was
            applied.
          </div>
        )}

        {data && groups && data.env.length > 0 && (
          <div className="space-y-3">
            {BUCKET_ORDER.map((bucket) => {
              const entries = groups[bucket]
              if (entries.length === 0) return null
              const info = BUCKET_INFO[bucket]
              return (
                <section key={bucket} className="border border-kb-border rounded-lg overflow-hidden">
                  <header className="px-3 py-2 bg-kb-elevated border-b border-kb-border">
                    <div className="flex items-center gap-2">
                      <BucketTag tag={info.tag} />
                      <h3 className="text-xs font-semibold text-kb-text-primary">{info.title}</h3>
                      <span className="text-[10px] text-kb-text-tertiary font-mono ml-auto">
                        {entries.length} var{entries.length === 1 ? '' : 's'}
                      </span>
                    </div>
                    <p className="text-[10px] text-kb-text-tertiary mt-1 leading-relaxed">
                      {info.subtitle}
                    </p>
                  </header>
                  <table className="w-full text-[11px]">
                    <tbody className="divide-y divide-kb-border">
                      {entries.map((e) => (
                        <tr key={e.name} className="hover:bg-kb-card-hover">
                          <td className="px-3 py-1.5 font-mono text-kb-text-primary whitespace-nowrap align-top">
                            {e.name}
                          </td>
                          <td className="px-3 py-1.5 font-mono text-kb-text-secondary break-all align-top">
                            {e.sensitive ? (
                              <span className="inline-flex items-center gap-1.5 text-kb-text-tertiary">
                                <EyeOff className="w-3 h-3" />
                                (set, redacted)
                              </span>
                            ) : (
                              <span className="inline-flex items-center gap-1.5">
                                <Eye className="w-3 h-3 text-kb-text-tertiary" />
                                {e.value === '' ? (
                                  <span className="italic text-kb-text-tertiary">(empty)</span>
                                ) : (
                                  e.value
                                )}
                              </span>
                            )}
                          </td>
                          <td className="px-3 py-1.5 text-right align-top whitespace-nowrap">
                            {e.configuredVia && (
                              <span className="inline-flex items-center gap-1 text-[10px] text-kb-accent">
                                <Pencil className="w-2.5 h-2.5" />
                                {e.configuredVia.label}
                              </span>
                            )}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </section>
              )
            })}
            <div className="text-[10px] font-mono text-kb-text-tertiary text-right">
              {data.count} variable{data.count === 1 ? '' : 's'} total
            </div>
          </div>
        )}
      </div>
    </Modal>
  )
}

function BucketTag({ tag }: { tag: 'BOOT-TIME' | 'RUNTIME' }) {
  if (tag === 'BOOT-TIME') {
    return (
      <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded-md text-[9px] font-mono font-semibold tracking-wider uppercase bg-kb-bg border border-kb-border text-kb-text-tertiary">
        <Info className="w-2.5 h-2.5" />
        Boot-time
      </span>
    )
  }
  return (
    <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded-md text-[9px] font-mono font-semibold tracking-wider uppercase bg-status-ok-dim/40 border border-status-ok-dim text-status-ok">
      <Pencil className="w-2.5 h-2.5" />
      Runtime
    </span>
  )
}
