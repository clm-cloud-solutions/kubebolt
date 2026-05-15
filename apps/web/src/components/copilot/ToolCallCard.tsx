import { useState } from 'react'
import { Wrench, ChevronDown, Check, AlertCircle, Loader2, Copy, Check as CheckSmall, ArrowUpRight } from 'lucide-react'
import { Link } from 'react-router-dom'
import { presentTool } from './toolPresenters'

interface Props {
  name: string
  // Tool input as the LLM passed it. Used to render the summary chip,
  // the kubectl/PromQL equivalent, and the input table on expand.
  input?: Record<string, unknown>
  // Result is consumed only as a status signal (defined = completed,
  // undefined = still running). The raw content is intentionally not
  // surfaced — the model already summarizes findings in its prose
  // response, and the JSON dump added more noise than signal.
  result?: string
  // Errored tools render with a red status icon and stay collapsed by
  // default; the operator can expand to verify the input that produced
  // the failure (and re-run via the kubectl equivalent if desired).
  isError?: boolean
}

// ToolCallCard renders one tool invocation as a persistent collapsible card
// in the chat stream. Lifecycle: loading (spinner, no expand) → success
// (green check, expand to see input + kubectl equivalent) or error (red x,
// expand to see the input that triggered the failure).
//
// Gated by `KUBEBOLT_AI_SHOW_TOOL_CALLS` (server-side) → `showToolCalls`
// (client config). The CopilotPanel decides whether to render these cards
// or fall back to the transient ToolCallIndicator.
export function ToolCallCard({ name, input, result, isError }: Props) {
  const isLoading = result === undefined
  const hasErrorBody = !!(isError && result)
  // Auto-expand on error so the operator sees what failed without an extra
  // click — errors are the one case where hidden detail is genuinely
  // actionable. Successes stay collapsed (still controlled by setOpen).
  const [open, setOpen] = useState(hasErrorBody)

  const presentation = presentTool(name, input)
  const hasExpandable =
    !isLoading &&
    (presentation.inputLines.length > 0 ||
      presentation.command !== '' ||
      presentation.link !== undefined ||
      hasErrorBody)
  const label = name.replace(/_/g, ' ')

  const StatusIcon = isLoading
    ? (props: { className?: string }) => <Loader2 className={`${props.className ?? ''} animate-spin`} />
    : isError
      ? AlertCircle
      : Check

  const statusColor = isLoading
    ? 'text-kb-text-tertiary'
    : isError
      ? 'text-status-error'
      : 'text-status-ok'

  return (
    <div className="rounded-lg bg-kb-bg border border-kb-border overflow-hidden text-[10px] font-mono">
      <button
        type="button"
        onClick={() => hasExpandable && setOpen((v) => !v)}
        disabled={!hasExpandable}
        className={`w-full flex items-center gap-2 px-3 py-1.5 text-kb-text-tertiary transition-colors ${
          hasExpandable ? 'hover:bg-kb-card-hover cursor-pointer' : 'cursor-default'
        }`}
      >
        <Wrench className="w-3 h-3 text-kb-accent shrink-0" />
        <span className="shrink-0">{label}</span>
        {presentation.summary && (
          <span className="text-kb-text-secondary truncate min-w-0">· {presentation.summary}</span>
        )}
        <StatusIcon className={`w-3 h-3 shrink-0 ml-auto ${statusColor}`} />
        {hasExpandable && (
          <ChevronDown
            className={`w-3 h-3 shrink-0 transition-transform ${open ? 'rotate-180' : ''}`}
          />
        )}
      </button>

      {open && hasExpandable && (
        <div className="border-t border-kb-border divide-y divide-kb-border">
          {hasErrorBody && (
            <div className="px-3 py-2">
              <div className="text-[9px] uppercase tracking-wider text-status-error mb-1.5">Error</div>
              <pre className="text-[11px] text-status-error whitespace-pre-wrap break-words leading-snug max-h-[200px] overflow-auto">
                {result}
              </pre>
            </div>
          )}
          {presentation.inputLines.length > 0 && (
            <div className="px-3 py-2">
              <div className="text-[9px] uppercase tracking-wider text-kb-text-tertiary mb-1.5">Input</div>
              <div className="space-y-0.5">
                {presentation.inputLines.map(({ key, value }) => (
                  <div key={key} className="flex gap-2 items-baseline text-[11px]">
                    <span className="text-kb-text-tertiary shrink-0">{key}:</span>
                    <span className="text-kb-text-primary whitespace-pre-wrap break-all">{value}</span>
                  </div>
                ))}
              </div>
            </div>
          )}
          {presentation.command && (
            <div className="px-3 py-2">
              <div className="flex items-center justify-between mb-1.5">
                <span className="text-[9px] uppercase tracking-wider text-kb-text-tertiary">Equivalent</span>
                <CopyButton value={presentation.command} />
              </div>
              <pre className="text-[11px] text-kb-text-secondary whitespace-pre-wrap break-all leading-snug">
                {presentation.command}
              </pre>
            </div>
          )}
          {presentation.link && (
            <Link
              to={presentation.link.href}
              className="flex items-center justify-between px-3 py-2 text-[11px] text-kb-accent hover:bg-kb-card-hover transition-colors"
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
      className="flex items-center gap-1 text-[10px] text-kb-text-tertiary hover:text-kb-text-primary transition-colors"
      aria-label={copied ? 'Copied' : 'Copy command'}
    >
      {copied ? <CheckSmall className="w-3 h-3 text-status-ok" /> : <Copy className="w-3 h-3" />}
      {copied ? 'Copied' : 'Copy'}
    </button>
  )
}
