import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, ArrowRight, Boxes, Copy, ExternalLink, Layers, Server, ShieldCheck, Wrench } from 'lucide-react'
import { useState } from 'react'
import { Modal } from '@/components/shared/Modal'
import { api } from '@/services/api'

// FindingDetailModal — the drill-down the table cannot carry.
//
// The row is a summary BY DESIGN: a finding's identity excludes the package
// name, so one CVE affecting several packages of a workload collapses into one
// row. That is right for a list — the operator has one problem there, not
// seventeen — but it means the Remediation in the table is an ARBITRARY one of
// them. Trivy reports CVE-2026-33814 in cilium seventeen times, once per
// affected binary; the table can say "upgrade stdlib" when the reachable path
// is golang.org/x/net.
//
// So this panel answers the question the row provokes and cannot settle: which
// packages, which of them have a fix, and what do I actually run to close it.
//
// Design notes:
//   - Tokens only from tailwind.config (kb-* / status-*). The first cut invented
//     kb-status-error and kb-bg-subtle, which do not exist, so Tailwind emitted
//     nothing and the dialog rendered unstyled white on the dark app.
//   - The package list is read from the cluster on click rather than persisted,
//     so it can be missing while the finding is not. That case is STATED, never
//     rendered as an empty list — "no packages" and "we could not look" must not
//     look the same.

type FindingRow = {
  fingerprint: string
  clusterId: string
  kind: string
  source: string
  severity: string
  title: string
  status: string
  resourceKind?: string
  resourceNamespace?: string
  resourceName?: string
  cisControl?: string
  remediation?: string
  firstSeen: string
  lastSeen: string
}

const SEV_PILL: Record<string, string> = {
  critical: 'bg-status-error-dim text-status-error',
  high: 'bg-status-warn-dim text-status-warn',
  medium: 'bg-status-info/15 text-status-info',
  low: 'bg-kb-elevated text-kb-text-tertiary',
}

function ago(iso?: string): string {
  if (!iso) return '—'
  const secs = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000)
  if (secs < 60) return `${Math.floor(secs)}s`
  if (secs < 3600) return `${Math.floor(secs / 60)}m`
  if (secs < 86400) return `${Math.floor(secs / 3600)}h`
  return `${Math.floor(secs / 86400)}d`
}

// FindingDetailBody is the panel WITHOUT its dialog chrome, so the workload
// view can render it inline after replacing its own content — two levels in
// one dialog rather than a modal stacked on a modal, which is hard to escape
// and loses the context the operator came from.
export function FindingDetailBody({ finding }: { finding: FindingRow }) {
  const isCVE = finding.kind === 'cve'
  const { data, isLoading, error } = useQuery({
    queryKey: ['finding-detail', finding.fingerprint, finding.clusterId],
    queryFn: () => api.getFindingDetail(finding.fingerprint, finding.clusterId),
    staleTime: 60_000,
  })

  const images = data?.images ?? []
  const pkgs = images.flatMap((c) => c.packages)
  const podsAffected = images.reduce((n, i) => n + (i.pods > 0 ? i.pods : 0), 0)
  const fixable = pkgs.filter((p) => p.fixedVersion)
  const worstScore = pkgs.reduce((m, p) => Math.max(m, p.score ?? 0), 0)

  return (
      <div className="flex-1 overflow-y-auto">
        {/* Identity strip — the four facts that place the finding, in the same
            mono/label grammar the rest of the app uses for metadata. */}
        <div className="grid grid-cols-2 sm:grid-cols-4 gap-px bg-kb-border border-b border-kb-border">
          <Fact label="Resource">
            {finding.resourceName ? (
              <>
                <span className="text-kb-text-primary">{finding.resourceName}</span>
                <span className="text-kb-text-tertiary">
                  {' '}
                  {finding.resourceKind}
                  {finding.resourceNamespace ? ` · ${finding.resourceNamespace}` : ''}
                </span>
              </>
            ) : (
              <span className="text-kb-text-tertiary">cluster-wide</span>
            )}
          </Fact>
          <Fact label="Source">
            <span className="text-kb-text-primary">{finding.source}</span>
            {finding.cisControl && (
              <span className="text-kb-text-tertiary"> · control {finding.cisControl}</span>
            )}
          </Fact>
          <Fact label="Open for">
            <span className="text-kb-text-primary">{ago(finding.firstSeen)}</span>
            <span className="text-kb-text-tertiary"> · since {new Date(finding.firstSeen).toLocaleDateString()}</span>
          </Fact>
          <Fact label="Last confirmed">
            <span className="text-kb-text-primary">{ago(finding.lastSeen)} ago</span>
          </Fact>
        </div>

        <div className="p-5 space-y-5">
          {/* Full title. The header truncates, and a CVE description is exactly
              the thing an operator wants to read whole before deciding. */}
          <p className="text-xs leading-relaxed text-kb-text-secondary">{finding.title}</p>

          {!isCVE ? (
            isLoading ? (
              <Notice tone="neutral">Reading the compliance report from the cluster…</Notice>
            ) : (
              <ComplianceBody
                detail={data?.compliance}
                live={!!data?.live}
                liveError={data?.liveError}
                fallbackRemediation={finding.remediation}
                findingSeverity={finding.severity}
              />
            )
          ) : isLoading ? (
            <Notice tone="neutral">Reading the scan report from the cluster…</Notice>
          ) : error || !data?.live ? (
            <Notice tone="warn">
              {data?.liveError || 'The cluster did not answer.'} The finding itself is stored, so
              what you see above is accurate — only the affected packages are unknown.
            </Notice>
          ) : images.length === 0 ? (
            <Notice tone="ok">
              The current scan report no longer lists this CVE for {finding.resourceName}. It was
              likely fixed after the last sweep, and the row will clear on the next one.
            </Notice>
          ) : (
            <>
              {/* The headline the operator needs before reading a table: can I
                  fix this at all, and how bad is it. */}
              <div className="flex flex-wrap items-center gap-x-5 gap-y-2 rounded-lg border border-kb-border bg-kb-elevated px-3 py-2.5">
                <Stat
                  icon={<Boxes className="h-3.5 w-3.5" />}
                  value={images.length}
                  label={images.length === 1 ? 'affected image' : 'affected images'}
                />
                {/* Blast radius. A CVE row describes an image; how much is
                    actually exposed is how many pods carry it right now, which
                    scaling changes and the finding does not. */}
                {podsAffected > 0 && (
                  <Stat
                    icon={<Server className="h-3.5 w-3.5" />}
                    value={podsAffected}
                    label={podsAffected === 1 ? 'pod running it' : 'pods running it'}
                    accent="text-status-warn"
                  />
                )}
                <Stat
                  icon={<Wrench className="h-3.5 w-3.5" />}
                  value={fixable.length}
                  label="with a fix available"
                  accent={fixable.length > 0 ? 'text-kb-accent' : 'text-kb-text-tertiary'}
                />
                {worstScore > 0 && (
                  <Stat
                    icon={<AlertTriangle className="h-3.5 w-3.5" />}
                    value={worstScore.toFixed(1)}
                    label="highest CVSS"
                    accent={worstScore >= 9 ? 'text-status-error' : 'text-status-warn'}
                  />
                )}
                {pkgs.length > 1 && (
                  <span className="text-[10px] text-kb-text-tertiary">
                    the table row shows only one of these
                  </span>
                )}
              </div>

              {fixable.length > 0 && (
                <Section title="What to do">
                  <p className="mb-2 text-xs leading-relaxed text-kb-text-secondary">
                    Rebuild{' '}
                    <code className="font-mono text-kb-text-primary">
                      {images[0]?.image || `${finding.resourceName}'s image`}
                    </code>{' '}
                    with {fixable.length === 1 ? 'this package' : 'these packages'} upgraded, then
                    roll out {finding.resourceName}. Patching the running container is not enough —
                    the vulnerability lives in the image, and the next pod would bring it back.
                  </p>
                  <CopyBlock
                    text={Array.from(
                      new Set(
                        fixable.map((p) => `${p.name} ${p.installedVersion ?? ''} → ${p.fixedVersion}`),
                      ),
                    ).join('\n')}
                  />
                </Section>
              )}

              {images.map((c) => (
                <Section
                  key={c.image}
                  title={c.containers.join(' · ') || 'container'}
                  aside={`${c.packages.length} pkg · ${c.packages.filter((p) => p.fixedVersion).length} fixable${
                    c.pods > 0 ? ` · ${c.pods} pod${c.pods === 1 ? '' : 's'}` : ''
                  }`}
                >
                  {/* The image IS the vulnerable thing — Trivy scans an image
                      mounted in a container, not an abstract workload. Naming it
                      first, in a form that can be pulled and rebuilt, is what
                      turns this panel from a report into an instruction. */}
                  <div className="mb-2 flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-kb-border bg-kb-elevated px-3 py-2">
                    <Layers className="h-3.5 w-3.5 shrink-0 text-kb-text-tertiary" />
                    <code className="font-mono text-[11px] text-kb-text-primary break-all">
                      {c.image || 'image unknown'}
                    </code>
                    {c.os && (
                      <span className="font-mono text-[10px] text-kb-text-tertiary">
                        base {c.os}
                      </span>
                    )}
                    {c.digest && (
                      <span
                        className="font-mono text-[10px] text-kb-text-tertiary"
                        title={c.digest}
                      >
                        {c.digest.slice(0, 19)}…
                      </span>
                    )}
                  </div>
                  <div className="overflow-x-auto rounded-lg border border-kb-border">
                    <table className="w-full text-left text-[11px]">
                      <thead className="bg-kb-elevated text-kb-text-tertiary">
                        <tr>
                          <th className="px-3 py-2 font-medium">Package</th>
                          <th className="px-3 py-2 font-medium">Installed</th>
                          <th className="px-3 py-2 font-medium">Fixed in</th>
                          <th className="px-3 py-2 font-medium text-right">CVSS</th>
                          <th className="px-3 py-2" />
                        </tr>
                      </thead>
                      <tbody>
                        {c.packages.map((p, i) => (
                          <tr
                            key={`${p.name}-${i}`}
                            className="border-t border-kb-border hover:bg-kb-card-hover transition-colors"
                          >
                            <td className="px-3 py-2 font-mono text-kb-text-primary break-all">
                              {p.name}
                            </td>
                            <td className="px-3 py-2 font-mono text-kb-text-tertiary">
                              {p.installedVersion || '—'}
                            </td>
                            <td className="px-3 py-2 font-mono">
                              {p.fixedVersion ? (
                                <span className="inline-flex items-center gap-1 text-kb-accent">
                                  <ArrowRight className="h-3 w-3" />
                                  {p.fixedVersion}
                                </span>
                              ) : (
                                <span className="text-kb-text-tertiary">no fix yet</span>
                              )}
                            </td>
                            <td className="px-3 py-2 text-right font-mono text-kb-text-secondary">
                              {p.score ? p.score.toFixed(1) : '—'}
                            </td>
                            <td className="px-3 py-2 text-right">
                              {p.link && (
                                <a
                                  href={p.link}
                                  target="_blank"
                                  rel="noreferrer noopener"
                                  title="Open the advisory"
                                  className="inline-flex text-kb-text-tertiary hover:text-kb-accent transition-colors"
                                >
                                  <ExternalLink className="h-3 w-3" />
                                </a>
                              )}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </Section>
              ))}

              {fixable.length === 0 && (
                <p className="text-[11px] text-kb-text-tertiary">
                  No upstream fix is published for any affected package yet. There is nothing to
                  upgrade to — track the advisory, or drop the dependency.
                </p>
              )}
            </>
          )}
        </div>
      </div>
  )
}

export function FindingDetailModal({ finding, onClose }: { finding: FindingRow; onClose: () => void }) {
  return (
    <Modal
      badge={finding.severity}
      badgeClass={SEV_PILL[finding.severity] ?? SEV_PILL.low}
      title={titleHead(finding.title)}
      onClose={onClose}
      size="2xl"
    >
      <FindingDetailBody finding={finding} />
    </Modal>
  )
}

// The header truncates, so give it the identifier rather than the prose — the
// CVE id is what an operator searches, pastes and recognizes.
function titleHead(title: string): string {
  const i = title.indexOf(':')
  return i > 0 ? title.slice(0, i) : title
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="bg-kb-card px-4 py-2.5 min-w-0">
      <div className="text-[9px] uppercase tracking-wider text-kb-text-tertiary">{label}</div>
      <div className="mt-0.5 font-mono text-[11px] truncate">{children}</div>
    </div>
  )
}

function Section({
  title,
  aside,
  children,
}: {
  title: string
  aside?: string
  children: React.ReactNode
}) {
  return (
    <div>
      <div className="mb-2 flex items-baseline justify-between gap-3">
        <h3 className="text-xs font-medium text-kb-text-primary">{title}</h3>
        {aside && <span className="font-mono text-[10px] text-kb-text-tertiary">{aside}</span>}
      </div>
      {children}
    </div>
  )
}

function Stat({
  icon,
  value,
  label,
  accent,
}: {
  icon: React.ReactNode
  value: number | string
  label: string
  accent?: string
}) {
  return (
    <span className="inline-flex items-baseline gap-1.5">
      <span className={`self-center ${accent ?? 'text-kb-text-tertiary'}`}>{icon}</span>
      <span className={`text-sm font-semibold tabular-nums ${accent ?? 'text-kb-text-primary'}`}>
        {value}
      </span>
      <span className="text-[10px] font-mono text-kb-text-tertiary">{label}</span>
    </span>
  )
}

function CopyBlock({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <div className="relative rounded-lg border border-kb-border bg-kb-elevated">
      <pre className="overflow-x-auto px-3 py-2.5 pr-10 font-mono text-[11px] leading-relaxed text-kb-text-secondary whitespace-pre">
        {text}
      </pre>
      <button
        type="button"
        onClick={() => {
          void navigator.clipboard?.writeText(text)
          setCopied(true)
          setTimeout(() => setCopied(false), 1500)
        }}
        className="absolute right-2 top-2 rounded p-1 text-kb-text-tertiary hover:text-kb-text-primary transition-colors"
        title="Copy"
      >
        {copied ? <ShieldCheck className="h-3.5 w-3.5 text-kb-accent" /> : <Copy className="h-3.5 w-3.5" />}
      </button>
    </div>
  )
}

// Notice states WHY something is absent. An empty table would read as "nothing
// to fix", which is the opposite of "we could not check".
function Notice({ children, tone }: { children: React.ReactNode; tone: 'neutral' | 'warn' | 'ok' }) {
  const toneClass =
    tone === 'warn'
      ? 'border-status-warn/30 bg-status-warn-dim text-kb-text-secondary'
      : tone === 'ok'
        ? 'border-status-ok/30 bg-kb-elevated text-kb-text-secondary'
        : 'border-kb-border bg-kb-elevated text-kb-text-tertiary'
  return (
    <p className={`rounded-lg border px-3 py-2.5 text-xs leading-relaxed ${toneClass}`}>{children}</p>
  )
}

export const sevPillClass = SEV_PILL

// ComplianceBody — the CIS side of the drill-down.
//
// The stored finding carries a count and nothing else, because that is all
// Trivy's summary report publishes: "42 failing" tells an operator there is work
// without saying where, which is the least useful shape a number can take. The
// names live one hop away, through the check id the control declares.
function ComplianceBody({
  detail,
  live,
  liveError,
  fallbackRemediation,
  findingSeverity,
}: {
  detail?: {
    benchmark?: string
    control?: string
    description?: string
    severity?: string
    failingTotal: number
    failingResources?: Array<{ kind?: string; namespace?: string; name?: string; message?: string }>
  }
  live: boolean
  liveError?: string
  fallbackRemediation?: string
  findingSeverity: string
}) {
  const rows = detail?.failingResources ?? []
  const listed = rows.length
  const total = detail?.failingTotal ?? 0

  return (
    <div className="space-y-5">
      {detail?.description && (
        <Section title="What the control requires">
          <p className="text-xs leading-relaxed text-kb-text-secondary">{detail.description}</p>
          <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-[10px] font-mono text-kb-text-tertiary">
            {detail.benchmark && <span>{detail.benchmark}</span>}
            {/* The benchmark's own rating can disagree with the finding's,
                because the normalizer defaults compliance findings to medium.
                Showing both is honest; silently picking one is not. */}
            {detail.severity && detail.severity !== findingSeverity && (
              <span>
                benchmark rates it <span className="text-kb-text-secondary">{detail.severity}</span>
              </span>
            )}
          </div>
        </Section>
      )}

      {!live ? (
        <Notice tone="warn">
          {liveError || 'The cluster did not answer.'} The control and its count are stored, so what
          you see above is accurate — only the list of failing resources is unavailable.
        </Notice>
      ) : rows.length === 0 ? (
        <Notice tone="neutral">
          {fallbackRemediation || 'This control is audited at node level, so it does not map to a list of resources.'}
        </Notice>
      ) : (
        <Section
          title="Resources failing this control"
          aside={listed < total ? `showing ${listed} of ${total}` : `${total}`}
        >
          <div className="overflow-x-auto rounded-lg border border-kb-border">
            <table className="w-full text-left text-[11px]">
              <thead className="bg-kb-elevated text-kb-text-tertiary">
                <tr>
                  <th className="px-3 py-2 font-medium">Resource</th>
                  <th className="px-3 py-2 font-medium">Kind</th>
                  <th className="px-3 py-2 font-medium">Namespace</th>
                  <th className="px-3 py-2 font-medium">Why</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r, i) => (
                  <tr
                    key={`${r.namespace}-${r.name}-${i}`}
                    className="border-t border-kb-border hover:bg-kb-card-hover transition-colors"
                  >
                    <td className="px-3 py-2 font-mono text-kb-text-primary break-all">{r.name}</td>
                    <td className="px-3 py-2 font-mono text-kb-text-tertiary">{r.kind || '—'}</td>
                    <td className="px-3 py-2 font-mono text-kb-text-tertiary">{r.namespace || '—'}</td>
                    <td className="px-3 py-2 text-kb-text-secondary">{r.message || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {listed < total && (
            <p className="mt-2 text-[11px] text-kb-text-tertiary">
              {total - listed} more fail the same control — a cluster-wide control routinely trips
              on most workloads, so the list is capped rather than endless.
            </p>
          )}
        </Section>
      )}
    </div>
  )
}
