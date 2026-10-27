import { useContext, useEffect, useRef, useState } from 'react'
import { QueryClientContext } from '@tanstack/react-query'
import {
  Wrench,
  ChevronDown,
  Check,
  AlertCircle,
  Loader2,
  Copy,
  Check as CheckSmall,
  ArrowUpRight,
  Ban,
  Square,
  type LucideIcon,
} from 'lucide-react'
import { Link } from 'react-router-dom'
import { presentTool } from './toolPresenters'
import { toolTitle } from './toolTitles'
import { connectorShortName, externalTool } from '@/utils/toolName'
import { CloudProviderIcon } from '@/components/shared/CloudProviderIcon'
import { MCP_CONNECTORS_KEY } from '@/components/admin/mcp/ConnectorBits'
import type { McpConnector } from '@/services/api'

interface Props {
  name: string
  // Tool input as the LLM passed it. Used to render the summary chip,
  // the kubectl/PromQL equivalent, and the input table on expand.
  input?: Record<string, unknown>
  // Result is consumed as a status signal (defined = completed,
  // undefined = still running). The raw content is intentionally not
  // surfaced — the model already summarizes findings in its prose
  // response, and the JSON dump added more noise than signal. The one
  // field read from it is get_external_connections' inFleet match, for the
  // chip (see toolPresenters).
  result?: string
  // Errored tools render with a red status icon and stay collapsed by
  // default; the operator can expand to verify the input that produced
  // the failure (and re-run via the kubectl equivalent if desired).
  isError?: boolean
  // How the call ended, as the server classified it (ok, empty, not_found,
  // denied, timeout, error, not_sent), and how long it took. Absent on
  // conversations stored before they existed: then isError decides.
  status?: string
  durationMs?: number
  // What came back, counted and listed by the server: the card's second line.
  digest?: string
  // For a call that did not read: the error's first sentence, from the server.
  reason?: string
  // No result and the turn is over (stopped, or it failed first): the card
  // says the call did not finish instead of spinning.
  stopped?: boolean
  // The operator stopped the turn: the card says so.
  stoppedByYou?: boolean
  // Admins get a link from a cloud connector's card to its settings; for
  // anyone else the AI hub would answer "access denied".
  canManageConnectors?: boolean
}

// ToolCallCard renders one tool invocation as a persistent collapsible card
// in the chat stream. Lifecycle: loading (spinner, no expand) → success
// (green check, expand to see input + kubectl equivalent) or error (red x,
// expand to see the input that triggered the failure).
//
// Gated by `KUBEBOLT_AI_SHOW_TOOL_CALLS` (server-side) → `showToolCalls`
// (client config). The CopilotPanel decides whether to render these cards
// or fall back to the transient ToolCallIndicator.
export function ToolCallCard({
  name,
  input,
  result,
  isError,
  status,
  durationMs,
  digest,
  reason,
  stopped,
  stoppedByYou,
  canManageConnectors,
}: Props) {
  const isLoading = result === undefined && !stopped
  const hasErrorBody = !!(isError && result)
  // Every card starts collapsed, errors too: an error body is often addressed
  // to the model ("the agent is not connected; tell the operator…"), and the
  // card's status already says the call failed. One click shows it.
  const [open, setOpen] = useState(false)

  // A running call counts its own time: a slow read (a describe over the
  // agent can run to its 90 s timeout) says so on its card, where the operator
  // is looking, instead of a "taking longer" line under it. The card mounts
  // when the call starts and stays mounted through `done`.
  const startedAt = useRef(Date.now())
  const [, setTick] = useState(0)
  useEffect(() => {
    if (!isLoading) return
    const id = window.setInterval(() => setTick((t) => t + 1), 1000)
    return () => window.clearInterval(id)
  }, [isLoading])
  const runningFor = isLoading ? Date.now() - startedAt.current : 0

  // A cloud call that named no region ran in the connector's. An admin who
  // has the connector list loaded knows which one, so the equivalent command
  // can name it; nobody else has it, and nothing is fetched for this. Read
  // through the context so a card rendered outside a query client still works.
  const queryClient = useContext(QueryClientContext)
  const connectorRegion = (id: string) =>
    canManageConnectors
      ? queryClient
          ?.getQueryData<{ connectors: McpConnector[] | null }>(MCP_CONNECTORS_KEY)
          ?.connectors?.find((c) => c.id === id)?.config.fields?.region || undefined
      : undefined

  const presentation = presentTool(name, input, {
    canManageConnectors,
    connectorRegion,
    result: isError ? undefined : result,
  })
  const hasExpandable =
    !isLoading &&
    (presentation.inputLines.length > 0 ||
      presentation.command !== '' ||
      presentation.link !== undefined ||
      hasErrorBody)
  // A cloud connector read is labelled by its provider ("AWS · call") with
  // the provider's glyph and colour, so a call that left the cluster is told
  // apart from KubeBolt's own reads at a glance. AWS's single `call` tool is
  // named by its operation in the chip, so the label is the provider alone.
  // The title says what is read and the chip what about ("Pods ·
  // catalog-api" [tienda]); a call that left the cluster is led by its
  // cloud's name in the cloud's colour. The tool's name and the long summary
  // stay in the tooltip.
  const ext = externalTool(name)
  const { title, chip } = toolTitle(name, input, isError ? undefined : result)
  const tooltip = [name, presentation.summary].filter(Boolean).join(' · ')
  const cloud = ext ? CLOUD_TOKENS[ext.connectorId] : undefined

  const outcome = isLoading
    ? RUNNING
    : stopped && result === undefined
      ? stoppedByYou
        ? STOPPED_BY_YOU
        : STOPPED
      : outcomeOf(status, isError)
  // A call that did not read says why on a line of its own, so the word by
  // the icon would only repeat it.
  const failure = isLoading ? undefined : failureLine(status, reason, durationMs)
  const StatusIcon = outcome.icon
  const elapsed = isLoading
    ? runningFor >= RUNNING_COUNTER_AFTER_MS
      ? formatDuration(runningFor)
      : ''
    : durationMs !== undefined
      ? formatDuration(durationMs)
      : ''

  return (
    <div
      className={`rounded-lg bg-kobi-bg border border-kobi-border overflow-hidden text-[10px] font-mono ${cloud ? 'border-l-2' : ''}`}
      style={cloud ? { borderLeftColor: `var(${cloud.edge})` } : undefined}
    >
      <button
        type="button"
        onClick={() => hasExpandable && setOpen((v) => !v)}
        disabled={!hasExpandable}
        className={`w-full flex items-center gap-2 px-3 py-1.5 text-kobi-text-tertiary transition-colors ${
          hasExpandable ? 'hover:bg-kobi-elevated cursor-pointer' : 'cursor-default'
        }`}
      >
        {ext ? (
          <span className="w-3 h-3 shrink-0 inline-flex items-center justify-center">
            <CloudProviderIcon provider={ext.connectorId} className="w-3 h-3" />
          </span>
        ) : (
          <Wrench className="w-3 h-3 text-kobi-accent shrink-0" />
        )}
        {ext && (
          <span className="shrink-0" style={cloud ? { color: `var(${cloud.text})` } : undefined}>
            {connectorShortName(ext.connectorId)}
          </span>
        )}
        <span className="text-kobi-text truncate min-w-0" title={tooltip}>
          {title}
        </span>
        {chip && (
          <span
            className="shrink min-w-0 max-w-[45%] truncate px-1.5 rounded bg-kobi-elevated text-kobi-text-secondary"
            title={chip}
          >
            {chip}
          </span>
        )}
        <span className="ml-auto flex items-center gap-1.5 shrink-0">
          {outcome.word && !failure && <span className={outcome.color}>{outcome.word}</span>}
          {elapsed && <span className="tabular-nums text-kobi-text-tertiary">{elapsed}</span>}
          <StatusIcon
            className={`w-3 h-3 shrink-0 ${outcome.color} ${isLoading ? 'animate-spin' : ''}`}
            style={isLoading && cloud ? { color: `var(${cloud.text})` } : undefined}
          />
        </span>
        {hasExpandable && (
          <ChevronDown
            className={`w-3 h-3 shrink-0 transition-transform ${open ? 'rotate-180' : ''}`}
          />
        )}
      </button>

      {failure && (
        <div className={`pl-8 pr-3 pb-1.5 -mt-0.5 truncate ${failure.color}`} title={failure.text}>
          {failure.text}
        </div>
      )}

      {digest && !isLoading && !failure && (
        // Under the title, aligned with it: what the call found, at a glance.
        <div className="pl-8 pr-3 pb-1.5 -mt-0.5 text-kobi-text-secondary truncate" title={formatDigest(digest)}>
          {formatDigest(digest)}
        </div>
      )}

      {open && hasExpandable && (
        <div className="border-t border-kobi-border divide-y divide-kb-border">
          {hasErrorBody && (
            <div className="px-3 py-2">
              <div className="text-[9px] uppercase tracking-wider text-kobi-st-error mb-1.5">Error</div>
              <pre className="text-[11px] text-kobi-st-error whitespace-pre-wrap break-words leading-snug max-h-[200px] overflow-auto">
                {result}
              </pre>
            </div>
          )}
          {presentation.inputLines.length > 0 && (
            <div className="px-3 py-2">
              <div className="text-[9px] uppercase tracking-wider text-kobi-text-tertiary mb-1.5">Input</div>
              <div className="space-y-0.5">
                {presentation.inputLines.map(({ key, value }) => (
                  <div key={key} className="flex gap-2 items-baseline text-[11px]">
                    <span className="text-kobi-text-tertiary shrink-0">{key}:</span>
                    <span className="text-kobi-text whitespace-pre-wrap break-all">{value}</span>
                  </div>
                ))}
              </div>
            </div>
          )}
          {presentation.command && (
            <div className="px-3 py-2">
              <div className="flex items-center justify-between mb-1.5">
                <span className="text-[9px] uppercase tracking-wider text-kobi-text-tertiary">Equivalent</span>
                <CopyButton value={presentation.command} />
              </div>
              <pre className="text-[11px] text-kobi-text-secondary whitespace-pre-wrap break-all leading-snug">
                {presentation.command}
              </pre>
            </div>
          )}
          {presentation.link && (
            <Link
              to={presentation.link.href}
              className="flex items-center justify-between px-3 py-2 text-[11px] text-kobi-accent hover:bg-kobi-elevated transition-colors"
            >
              <span>{presentation.link.label}</span>
              <ArrowUpRight className="w-3 h-3" />
            </Link>
          )}
        </div>
      )}
    </div>
  )
}

// The card's edge and label take the cloud's own colour (tokens in
// globals.css, from CloudProviderIcon's brand tints; the text variants keep
// 4.5:1 on the panel). The colour says WHERE the call went; how it ended stays
// in the state colours, so AWS orange never reads as a warning.
export const CLOUD_TOKENS: Record<string, { edge: string; text: string }> = {
  aws: { edge: '--kobi-cloud-aws', text: '--kobi-cloud-aws-text' },
  azure: { edge: '--kobi-cloud-azure', text: '--kobi-cloud-azure-text' },
  gcp: { edge: '--kobi-cloud-gcp', text: '--kobi-cloud-gcp-text' },
}

interface Outcome {
  icon: LucideIcon
  color: string
  // Said next to the icon when the icon alone would not tell it.
  word?: string
}

const RUNNING: Outcome = { icon: Loader2, color: 'text-kobi-text-tertiary' }
// Most reads come back in well under this; past it the running card counts.
const RUNNING_COUNTER_AFTER_MS = 3000
const STOPPED: Outcome = { icon: Square, color: 'text-kobi-text-tertiary', word: 'not finished' }
const STOPPED_BY_YOU: Outcome = { icon: Square, color: 'text-kobi-text-tertiary', word: 'stopped' }

function outcomeOf(status: string | undefined, isError: boolean | undefined): Outcome {
  switch (status) {
    case 'ok':
      return { icon: Check, color: 'text-kobi-st-ok' }
    case 'empty':
      return { icon: Check, color: 'text-kobi-text-tertiary', word: 'empty' }
    case 'not_found':
      return { icon: Check, color: 'text-kobi-text-tertiary', word: 'not found' }
    case 'denied':
      return { icon: AlertCircle, color: 'text-kobi-st-warn', word: 'denied' }
    case 'timeout':
      return { icon: AlertCircle, color: 'text-kobi-st-error', word: 'timed out' }
    case 'not_sent':
      return { icon: Ban, color: 'text-kobi-text-tertiary', word: 'not sent' }
    case 'agent_offline':
      return { icon: AlertCircle, color: 'text-kobi-st-warn', word: 'agent offline' }
    case 'unsupported':
      return { icon: Ban, color: 'text-kobi-text-tertiary', word: 'not available' }
    case 'error':
      return { icon: AlertCircle, color: 'text-kobi-st-error' }
  }
  return isError ? { icon: AlertCircle, color: 'text-kobi-st-error' } : { icon: Check, color: 'text-kobi-st-ok' }
}

// failureLine is the line under the title of a call that did not read, in
// the colour of how it ended: red when it failed, amber when it could not read
// through no fault of the call, grey when nothing was tried.
function failureLine(
  status: string | undefined,
  reason: string | undefined,
  durationMs: number | undefined,
): { text: string; color: string } | undefined {
  const withReason = (lead: string) => (reason ? `${lead} — ${reason}` : lead)
  switch (status) {
    case 'error':
      return { text: withReason('Failed'), color: 'text-kobi-st-error' }
    case 'timeout':
      return {
        text: durationMs !== undefined ? `Timed out after ${formatDuration(durationMs)}` : 'Timed out',
        color: 'text-kobi-st-error',
      }
    case 'denied':
      return { text: withReason('Denied'), color: 'text-kobi-st-warn' }
    case 'agent_offline':
      return { text: 'Agent disconnected — no live reads until it reconnects', color: 'text-kobi-st-warn' }
    case 'not_sent':
      return { text: withReason('Not sent'), color: 'text-kobi-text-tertiary' }
    case 'unsupported':
      return { text: 'Not available for this kind', color: 'text-kobi-text-tertiary' }
    case 'not_found':
      return reason ? { text: `Not found — ${reason}`, color: 'text-kobi-text-tertiary' } : undefined
  }
  return undefined
}

// The server marks times in a digest as "@" + RFC 3339; they read in the
// operator's zone: 22:05 today, "Oct 22 22:05" otherwise.
const DIGEST_TIME = /@(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z)/g

export function formatDigest(digest: string, now: Date = new Date()): string {
  return digest.replace(DIGEST_TIME, (_, iso: string) => {
    const d = new Date(iso)
    if (Number.isNaN(d.getTime())) return iso
    const time = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false })
    if (d.toDateString() === now.toDateString()) return time
    return `${d.toLocaleDateString([], { month: 'short', day: 'numeric' })} ${time}`
  })
}

// 340 ms · 2.4 s · 12 s
export function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.max(0, Math.round(ms))} ms`
  if (ms < 10000) return `${(ms / 1000).toFixed(1)} s`
  return `${Math.round(ms / 1000)} s`
}

// Small inline copy button. Flashes to a check for ~1.4s after a successful
// copy so the operator gets visual confirmation without a toast.
function CopyButton({ value }: { value: string }) {
  const [copied, setCopied] = useState(false)
  const onClick = async () => {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      setTimeout(() => setCopied(false), 1400)
    } catch {
      // Clipboard API blocked (unfocused frame, insecure context). Silently
      // skip — the operator can still select and copy the text manually.
    }
  }
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex items-center gap-1 text-[10px] text-kobi-text-tertiary hover:text-kobi-text transition-colors"
      aria-label={copied ? 'Copied' : 'Copy command'}
    >
      {copied ? <CheckSmall className="w-3 h-3 text-kobi-st-ok" /> : <Copy className="w-3 h-3" />}
      {copied ? 'Copied' : 'Copy'}
    </button>
  )
}
