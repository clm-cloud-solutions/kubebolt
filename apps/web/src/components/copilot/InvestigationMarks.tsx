import { useContext, useEffect, useState } from 'react'
import { QueryClientContext } from '@tanstack/react-query'
import { AlertCircle, BookOpen, ChevronDown, ListChecks, Loader2, Square } from 'lucide-react'
import { CloudProviderIcon } from '@/components/shared/CloudProviderIcon'
import { MCP_CONNECTORS_KEY } from '@/components/admin/mcp/ConnectorBits'
import { connectorShortName } from '@/utils/toolName'
import type { McpConnector } from '@/services/api'
import type { CopilotToolCall, CopilotToolResult } from '@/services/copilot/types'
import { CLOUD_TOKENS } from './ToolCallCard'
import {
  cloudScope,
  couldNotRead,
  formatClock,
  guideLabel,
  type InvestigationRun,
  type LiveInvestigation,
} from './investigationRun'

// The marks of an investigation that are not reads: where it leaves the
// cluster, and the summary it folds into when it ends. Lighter than a tool
// card; the cloud colour says where, as on the cards.

// The connector's settings, for an admin who has them loaded: nobody else
// can, and nothing is fetched for this. Read through the context so a mark
// rendered outside a query client still works.
function useConnectorFields(id: string, canManageConnectors?: boolean): Record<string, string> | undefined {
  const queryClient = useContext(QueryClientContext)
  if (!canManageConnectors) return undefined
  return queryClient
    ?.getQueryData<{ connectors: McpConnector[] | null }>(MCP_CONNECTORS_KEY)
    ?.connectors?.find((c) => c.id === id)?.config.fields ?? undefined
}

// CloudBoundary sits before the first read of a connector in a question:
// "Leaves the cluster → AWS us-east-2 · read-only".
export function CloudBoundary({
  connectorId,
  input,
  canManageConnectors,
}: {
  connectorId: string
  input?: Record<string, unknown>
  canManageConnectors?: boolean
}) {
  const fields = useConnectorFields(connectorId, canManageConnectors)
  const scope = cloudScope(connectorId, input, fields)
  const cloud = CLOUD_TOKENS[connectorId]
  return (
    <div className="flex items-center gap-1.5 pt-1 min-w-0 text-[10px] font-mono text-kobi-text-tertiary">
      <span className="shrink-0">Leaves the cluster →</span>
      <CloudProviderIcon provider={connectorId} className="w-3 h-3 shrink-0" />
      <span className="shrink-0" style={cloud ? { color: `var(${cloud.text})` } : undefined}>
        {connectorShortName(connectorId)}
      </span>
      <span className="truncate min-w-0">{[scope, 'read-only'].filter(Boolean).join(' · ')}</span>
    </div>
  )
}

// InvestigationSummary heads a finished investigation: how long it took and
// what it read. The steps fold under it, above the answer; Show steps opens
// them with every card as it was. It sits a step above those cards — the card
// surface, an accent-tinted border, its label in the primary text — so with
// the steps open it still reads as their heading.
export function InvestigationSummary({
  run,
  open,
  onToggle,
}: {
  run: InvestigationRun
  open: boolean
  onToggle: () => void
}) {
  const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`
  return (
    <div
      className="rounded-lg bg-kobi-card border border-kobi-border text-[10px] font-mono"
      style={{ borderColor: 'color-mix(in srgb, var(--kobi-accent) 35%, var(--kobi-border))' }}
    >
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="w-full flex items-center gap-2 px-3 py-1.5 text-kobi-text-tertiary hover:bg-kobi-elevated transition-colors rounded-lg"
      >
        <ListChecks className="w-3 h-3 text-kobi-accent shrink-0" />
        <span className="font-medium text-kobi-text">Investigation</span>
        {run.elapsedMs !== undefined && <span className="tabular-nums">{formatClock(run.elapsedMs)}</span>}
        <span className="ml-auto flex items-center gap-1 shrink-0">
          {open ? 'Hide steps' : 'Show steps'}
          <ChevronDown className={`w-3 h-3 transition-transform ${open ? 'rotate-180' : ''}`} />
        </span>
      </button>
      <div className="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 px-3 pb-1.5 text-kobi-text-tertiary">
        {run.pausedAtLimit !== undefined && (
          <span className="text-kobi-st-warn">
            Paused at the step limit{run.pausedAtLimit > 0 ? ` · ${run.pausedAtLimit} steps` : ''} ·
          </span>
        )}
        <span>{plural(run.reads, 'read', 'reads')}</span>
        {run.byCloud.map(([id, n]) => {
          const cloud = CLOUD_TOKENS[id]
          return (
            <span key={id} className="flex items-center gap-1">
              <span aria-hidden>·</span>
              <CloudProviderIcon provider={id} className="w-3 h-3" />
              <span style={cloud ? { color: `var(${cloud.text})` } : undefined}>
                {n} in {connectorShortName(id)}
              </span>
            </span>
          )
        })}
        {run.guides > 0 && <span>· {plural(run.guides, 'guide', 'guides')}</span>}
        {run.charts > 0 && <span>· {plural(run.charts, 'chart', 'charts')}</span>}
        {run.proposals > 0 && <span>· {plural(run.proposals, 'proposal', 'proposals')}</span>}
        {run.unread > 0 && <span className="text-kobi-st-warn">· {run.unread} could not be read</span>}
      </div>
    </div>
  )
}

// TurnFailure is the last row of a question that ended in an error before it
// answered: "Kobi couldn't finish 0:58", and under it the error as the server
// words it. It takes a card's place, so the steps above stay as they were.
export function TurnFailure({ message, afterMs }: { message: string; afterMs?: number }) {
  return (
    <div className="rounded-lg bg-kobi-bg border border-kobi-border overflow-hidden text-[10px] font-mono">
      <div className="flex items-center gap-2 px-3 py-1.5 text-kobi-text-tertiary">
        <AlertCircle className="w-3 h-3 shrink-0 text-kobi-st-error" aria-hidden />
        <span className="text-kobi-text">Kobi couldn't finish</span>
        {afterMs !== undefined && <span className="ml-auto tabular-nums">{formatClock(afterMs)}</span>}
      </div>
      <div className="pl-8 pr-3 pb-1.5 -mt-0.5 text-kobi-st-error break-words">{message}</div>
    </div>
  )
}

// GuideLine replaces the card of a call that loads instructions instead of
// reading anything — a skill, or the list of an Azure command group: "Guide ·
// aws-investigation". It is not a read, so it says nothing about results.
export function GuideLine({
  call,
  result,
  running,
}: {
  call: CopilotToolCall
  result?: CopilotToolResult
  running: boolean
}) {
  return (
    <div className="flex items-center gap-1.5 min-w-0 text-[10px] font-mono text-kobi-text-tertiary">
      <BookOpen className="w-3 h-3 shrink-0" aria-hidden />
      <span className="shrink-0">Guide ·</span>
      <span className="truncate min-w-0 text-kobi-text-secondary">{guideLabel(call)}</span>
      {running && <Loader2 className="w-3 h-3 shrink-0 animate-spin" aria-label="Loading" />}
      {couldNotRead(result) && <span className="shrink-0 text-kobi-st-warn">· not loaded</span>}
    </div>
  )
}

// InvestigationStrip sits above the input while a question runs: how long,
// how many reads, and a badge for each cloud the question has read, which
// says "now" and pulses while a call to it is in flight. Stop lives here.
export function InvestigationStrip({ live, onStop }: { live?: LiveInvestigation; onStop: () => void }) {
  const [, setTick] = useState(0)
  useEffect(() => {
    const id = window.setInterval(() => setTick((t) => t + 1), 1000)
    return () => window.clearInterval(id)
  }, [])
  const elapsed = live ? Date.now() - live.startedAt.getTime() : 0
  const reads = live
    ? [
        `${live.reads} read${live.reads === 1 ? '' : 's'}`,
        ...live.byCloud.map(([id, n]) => `${n} in ${connectorShortName(id)}`),
      ].join(' · ')
    : ''
  return (
    <div className="flex items-center gap-2 mb-2 pl-2.5 pr-2 py-1.5 rounded-lg bg-kobi-card border border-kobi-border text-[12px]">
      <span
        className="w-[7px] h-[7px] rounded-full shrink-0 bg-kobi-sigil-investigating"
        style={{ boxShadow: '0 0 0 3px color-mix(in srgb, var(--kobi-sigil-investigating) 16%, transparent)' }}
        aria-hidden
      />
      <span className="flex flex-wrap items-center gap-x-2 gap-y-1 min-w-0">
        <span className="font-medium text-kobi-text">Investigating</span>
        <span className="font-mono text-[11.5px] tabular-nums text-kobi-text-secondary">{formatClock(elapsed)}</span>
        {live && live.reads > 0 && <span className="text-kobi-text-tertiary">· {reads}</span>}
        {live?.byCloud.map(([id]) => (
          <CloudBadge key={id} connectorId={id} now={live.cloudsNow.includes(id)} />
        ))}
      </span>
      <button
        type="button"
        onClick={onStop}
        title="Stop generating"
        className="ml-auto shrink-0 inline-flex items-center gap-[5px] px-2 py-[3px] rounded-md bg-kobi-elevated border border-kobi-border text-[11.5px] text-kobi-text-secondary hover:text-kobi-text transition-colors"
      >
        <Square className="w-2.5 h-2.5 fill-current" />
        Stop
      </button>
    </div>
  )
}

// CloudBadge: the cloud's glyph and name in a pill of its colour (the design's
// .badge): tinted fill, a border at a third, the readable text variant.
export function CloudBadge({ connectorId, now }: { connectorId: string; now?: boolean }) {
  const cloud = CLOUD_TOKENS[connectorId]
  const style = cloud
    ? {
        color: `var(${cloud.text})`,
        background: `color-mix(in srgb, var(${cloud.edge}) 10%, transparent)`,
        borderColor: `color-mix(in srgb, var(${cloud.edge}) 34%, transparent)`,
      }
    : undefined
  return (
    <span
      className={`inline-flex items-center gap-1 px-[5px] py-px rounded border border-kobi-border font-mono text-[9.5px] font-medium tracking-[.06em] whitespace-nowrap shrink-0 ${now ? 'animate-pulse' : ''}`}
      style={style}
    >
      <CloudProviderIcon provider={connectorId} className="w-2.5 h-2.5" />
      {connectorShortName(connectorId)}
      {now && ' now'}
    </span>
  )
}
