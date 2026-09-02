import { Modal } from '@/components/shared/Modal'

// RuntimeEventModal — what actually happened, for one Falco alert.
//
// The feed could only ever show four things: priority, rule name, pod and age.
// Everything that answers "and then what" was arriving and being thrown away —
// the command that ran, the file it touched, the user it ran as, the image it
// ran from, and the MITRE technique the rule maps to. A runtime alert you
// cannot open is an alert you cannot triage.
//
// The order below is the order a responder needs it in:
//
//   1. WHAT Falco wrote — its own sentence, before any of our formatting.
//   2. WHY it matters — the ATT&CK technique.
//   3. WHERE — cluster, namespace, pod, container, image, node.
//   4. WHAT RAN — process, command line, binary, user.
//   5. Everything else, verbatim, for whoever needs the field we did not think of.
//
// Deliberately NOT a "resolve" or "acknowledge" surface. Events are a
// point-in-time stream, not a worklist — nothing here has a lifecycle to close,
// and a button implying otherwise would lie about what the store can do.

// THE priority palette — one definition feeding the pill, the ring and the
// filter chips. There used to be two maps, and the shorter one was missing
// `Notice`: the same event came out blue in the ring and grey in the table,
// which does not read as a style slip but as a disagreement about the data.
//
// Falco grades on the syslog ladder — eight levels — and the app has four status
// colours. The mapping is POSITIONAL, not semantic: red for the top of the
// ladder, amber for the middle, blue below it, grey for the floor. Inside this
// lens that is unambiguous because the word is always printed next to the
// colour, in the pill and in the ring's legend.
//
// Blue therefore means Notice here and Medium on the other tabs. That is
// deliberate: they are different ladders, they never share a screen, and the
// alternative — minting a second palette — would add colours nobody has learnt
// for a gain the printed word already provides.
//
// KNOWN COMPRESSION: Emergency, Alert and Critical all render red, so the top
// three levels look alike. Falco's default rules top out at Critical, so it
// costs nothing today; a ruleset that uses Emergency would want them told apart.
export const RUNTIME_PRIORITIES = [
  { key: 'Emergency', pill: 'bg-status-error-dim text-status-error', stroke: 'stroke-status-error', swatch: 'bg-status-error' },
  { key: 'Alert', pill: 'bg-status-error-dim text-status-error', stroke: 'stroke-status-error', swatch: 'bg-status-error' },
  { key: 'Critical', pill: 'bg-status-error-dim text-status-error', stroke: 'stroke-status-error', swatch: 'bg-status-error' },
  { key: 'Error', pill: 'bg-status-warn-dim text-status-warn', stroke: 'stroke-status-warn', swatch: 'bg-status-warn' },
  { key: 'Warning', pill: 'bg-status-warn-dim text-status-warn', stroke: 'stroke-status-warn', swatch: 'bg-status-warn' },
  { key: 'Notice', pill: 'bg-status-info/15 text-status-info', stroke: 'stroke-status-info', swatch: 'bg-status-info' },
  { key: 'Informational', pill: 'bg-kb-elevated text-kb-text-tertiary', stroke: 'stroke-kb-text-tertiary', swatch: 'bg-kb-text-tertiary' },
  { key: 'Debug', pill: 'bg-kb-elevated text-kb-text-tertiary', stroke: 'stroke-kb-text-tertiary', swatch: 'bg-kb-text-tertiary' },
] as const

// priorityPill is the lookup every renderer goes through, so an unknown level
// from a custom ruleset degrades to neutral in ALL of them at once instead of
// one place deciding differently from another.
export function priorityPill(priority: string): string {
  return (
    RUNTIME_PRIORITIES.find((p) => p.key === priority)?.pill ??
    'bg-kb-elevated text-kb-text-tertiary'
  )
}

const PRIORITY_PILL = Object.fromEntries(
  RUNTIME_PRIORITIES.map((p) => [p.key, p.pill]),
) as Record<string, string>

export type RuntimeEvent = {
  id: string
  clusterId?: string
  at: string
  priority: string
  ruleName: string
  namespace?: string
  podName?: string
  detectedBehavior: string
  source: string
  fields?: Record<string, string>
}

// Fields promoted out of the raw dump into named rows. The rest still ships
// below — promoting is about reading order, never about hiding.
const WHERE = ['hostname', 'container.name', 'container.image.repository', 'container.image.tag']
const WHAT = ['proc.name', 'proc.cmdline', 'proc.exepath', 'proc.pname', 'user.name', 'user.uid']
// The OBJECT of the alert — what was touched, and how. Falco puts these first in
// its own output line (`file=/etc/shadow evt_type=openat …`) and they were
// landing in the alphabetical dump at the bottom, so the rule's subject read as
// a footnote. `fd.name` is a file for file rules and a socket for network ones;
// the label follows what arrived rather than guessing.
const TARGET = ['fd.name', 'evt.type', 'evt.args']
const PROMOTED = new Set([...WHERE, ...WHAT, ...TARGET, 'tags', 'k8s.ns.name', 'k8s.pod.name'])

function Row({ label, value, mono = true }: { label: string; value?: string; mono?: boolean }) {
  if (!value) return null
  return (
    <div className="flex items-baseline gap-3 py-1">
      <span className="w-36 shrink-0 text-[10px] font-mono uppercase tracking-wide text-kb-text-tertiary">
        {label}
      </span>
      <span
        className={`min-w-0 flex-1 text-[11px] text-kb-text-secondary break-all ${mono ? 'font-mono' : ''}`}
      >
        {value}
      </span>
    </div>
  )
}

const FALCO_PRIORITIES = new Set([
  'Emergency',
  'Alert',
  'Critical',
  'Error',
  'Warning',
  'Notice',
  'Informational',
  'Debug',
])

// falcoSentence pulls the human half out of Falco's output line.
//
// The line is `HH:MM:SS.nanos: <Priority> <sentence> | k=v k=v k=v…` — twelve
// useful words followed by the same fields this dialog already renders
// structured, plus `<NA>` placeholders for every field the rule did not fill.
// Showing it whole was a mistake: "unedited" protected Falco's wording but
// buried it, and a responder reads this paragraph first.
//
// Everything after the pipe stays reachable under All fields, so nothing is
// hidden — only reordered by usefulness. Falls back to the raw line whenever
// the shape is not what we expect, because a rule we have not seen must never
// render blank.
export function falcoSentence(output: string): string {
  const human = output.split(' | ')[0] ?? output
  // Drop the leading timestamp (the formatted date sits right below it) and the
  // priority word (it is already the badge in the header).
  const stripped = human.replace(/^\d{2}:\d{2}:\d{2}\.\d+:\s*/, '')
  // Only an actual Falco priority is stripped. Matching "any capitalised first
  // word" ate the first word of "Unexpected outbound connection" — the kind of
  // mangling that makes a security alert read as a different alert.
  const words = stripped.split(' ')
  const rest = words.length > 1 && FALCO_PRIORITIES.has(words[0]) ? words.slice(1).join(' ') : stripped
  return rest.trim() || output
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="px-5 py-3 border-t border-kb-border">
      <h3 className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary mb-1.5">
        {title}
      </h3>
      {children}
    </div>
  )
}

export function RuntimeEventModal({
  event,
  clusterName,
  onClose,
}: {
  event: RuntimeEvent
  clusterName?: string
  onClose: () => void
}) {
  const f = event.fields ?? {}
  // Falco ships tags as one comma-joined string; the MITRE ones are the reason
  // this section exists, so they lead and the housekeeping tags follow.
  const tags = (f.tags ?? '').split(',').filter(Boolean)
  const mitre = tags.filter((t) => t.startsWith('T') || t.startsWith('mitre_'))
  const other = tags.filter((t) => !mitre.includes(t))

  const rest = Object.entries(f)
    .filter(([k, v]) => !PROMOTED.has(k) && v && v !== '<nil>')
    .sort(([a], [b]) => a.localeCompare(b))

  const image = f['container.image.repository']
    ? f['container.image.repository'] + (f['container.image.tag'] ? `:${f['container.image.tag']}` : '')
    : ''

  return (
    <Modal
      badge={event.priority}
      badgeClass={PRIORITY_PILL[event.priority] ?? 'bg-kb-elevated text-kb-text-tertiary'}
      title={event.ruleName}
      onClose={onClose}
      size="2xl"
    >
      <div className="flex-1 overflow-y-auto">
        {/* What happened, in Falco's own words — the sentence only, at a size
            that says "read this first". */}
        <div className="px-5 py-4">
          <p className="text-sm text-kb-text-primary leading-relaxed break-words">
            {falcoSentence(event.detectedBehavior)}
          </p>
          <p className="mt-2 text-[11px] font-mono text-kb-text-tertiary">
            {new Date(event.at).toLocaleString()}
          </p>
        </div>

        {mitre.length > 0 && (
          <Section title="ATT&CK">
            <div className="flex flex-wrap gap-1.5">
              {mitre.map((t) => (
                <span
                  key={t}
                  className="px-2 py-0.5 rounded-full text-[10px] font-mono bg-status-error-dim text-status-error"
                >
                  {t}
                </span>
              ))}
              {other.map((t) => (
                <span
                  key={t}
                  className="px-2 py-0.5 rounded-full text-[10px] font-mono bg-kb-elevated text-kb-text-tertiary"
                >
                  {t}
                </span>
              ))}
            </div>
          </Section>
        )}

        <Section title="Where">
          {/* An event with no cluster is not a rendering gap to paper over: the
              ingest token it arrived on is not tied to a cluster, so in a fleet
              nothing can say which one this came from. Said in the operator's
              terms, with the fix, rather than as the internal word for it. */}
          {clusterName || event.clusterId ? (
            <Row label="Cluster" value={clusterName || event.clusterId} />
          ) : (
            <div className="flex items-baseline gap-3 py-1">
              <span className="w-36 shrink-0 text-[10px] font-mono uppercase tracking-wide text-kb-text-tertiary">
                Cluster
              </span>
              <span className="min-w-0 flex-1 text-[11px] text-status-warn">
                Not recorded — this ingest token is not tied to a cluster. Issue one scoped to a
                cluster so runtime events can be attributed.
              </span>
            </div>
          )}
          <Row label="Namespace" value={event.namespace} />
          <Row label="Pod" value={event.podName} />
          <Row label="Container" value={f['container.name']} />
          <Row label="Image" value={image} />
          <Row label="Node" value={f.hostname} />
        </Section>

        {(f['fd.name'] || f['evt.type']) && (
          <Section title="What was touched">
            <Row label={f['fd.name']?.startsWith('/') ? 'File' : 'Target'} value={f['fd.name']} />
            <Row label="Syscall" value={f['evt.type']} />
            <Row label="Arguments" value={f['evt.args']} />
          </Section>
        )}

        {(f['proc.cmdline'] || f['proc.name']) && (
          <Section title="What ran">
            <Row label="Command" value={f['proc.cmdline']} />
            <Row label="Process" value={f['proc.name']} />
            <Row label="Binary" value={f['proc.exepath']} />
            <Row label="Parent" value={f['proc.pname']} />
            <Row
              label="User"
              value={f['user.name'] ? `${f['user.name']}${f['user.uid'] ? ` (uid ${f['user.uid']})` : ''}` : ''}
            />
          </Section>
        )}

        {rest.length > 0 && (
          <Section title="All fields">
            <div className="rounded-lg border border-kb-border divide-y divide-kb-border">
              {rest.map(([k, v]) => (
                <div key={k} className="flex items-baseline gap-3 px-3 py-1.5">
                  <span className="w-44 shrink-0 text-[10px] font-mono text-kb-text-tertiary break-all">
                    {k}
                  </span>
                  <span className="min-w-0 flex-1 text-[11px] font-mono text-kb-text-secondary break-all">
                    {v}
                  </span>
                </div>
              ))}
            </div>
          </Section>
        )}

        <Section title="Raw alert">
          {/* Verbatim, because an incident write-up quotes the tool and not our
              paraphrase of it. Secondary position, not suppressed. */}
          <pre className="text-[10px] font-mono text-kb-text-tertiary whitespace-pre-wrap break-all">
            {event.detectedBehavior}
          </pre>
        </Section>

        <div className="px-5 py-3 border-t border-kb-border">
          <p className="text-[11px] text-kb-text-tertiary">
            Detected by {event.source}. Runtime events are a point-in-time stream — they age out of
            the feed rather than being resolved.
          </p>
        </div>
      </div>
    </Modal>
  )
}
