import { useLayoutEffect, useRef, useState } from 'react'
import type { McpSetupStep } from '@/services/api'
import { CopyButton } from './ConnectorBits'

// The connector's manual: what to create on the customer's side (a role and
// its read-only policy, a service account, a sidecar identity). The backend
// generates the policy JSON from the same tables as the tool allowlist, so the
// permissions the admin grants are the ones the connector uses — no more.
//
// Only the steps that follow each other are numbered. An alternative ("Or use
// this machine's credentials") replaces the steps before it, so it carries an
// "Or" instead of the next number, which would read as one more thing to do.
export function SetupGuide({ steps }: { steps: McpSetupStep[] }) {
  if (steps.length === 0) {
    return <p className="text-[11px] text-kb-text-tertiary">This connector needs no setup on your side.</p>
  }
  let n = 0
  return (
    <ol className="space-y-4">
      {steps.map((step, i) => {
        const marker = step.alternative ? 'Or' : String(++n)
        return (
          <li key={`${i}-${step.title}`} className="flex gap-3">
            <span
              className="w-5 h-5 mt-px rounded-full bg-kb-elevated border border-kb-border text-[10px] font-mono font-semibold text-kb-text-secondary flex items-center justify-center shrink-0"
              aria-hidden
            >
              {marker}
            </span>
            <div className="min-w-0 flex-1 space-y-1.5">
              <div className="text-xs font-semibold text-kb-text-primary">{step.title}</div>
              {step.body && (
                <p className="text-[11px] text-kb-text-secondary leading-relaxed whitespace-pre-line">
                  <InlineCode text={step.body} />
                </p>
              )}
              {step.code && <CodeBlock code={step.code} language={step.language} title={step.title} />}
            </div>
          </li>
        )
      })}
    </ol>
  )
}

// Step text may name a command in backticks (`aws sts get-caller-identity`).
// Odd pieces of the split are the code; an unmatched backtick stays a
// character rather than swallowing the rest of the sentence.
function InlineCode({ text }: { text: string }) {
  const parts = text.split('`')
  if (parts.length % 2 === 0) {
    const tail = parts.splice(-2, 2)
    parts.push(tail.join('`'))
  }
  if (parts.length < 3) return <>{text}</>
  return (
    <>
      {parts.map((part, i) =>
        i % 2 === 1 ? (
          <code
            key={i}
            className="px-1 rounded bg-kb-elevated font-mono text-kb-text-primary whitespace-nowrap"
          >
            {part}
          </code>
        ) : (
          part
        ),
      )}
    </>
  )
}

const LANGUAGE: Record<string, { tag: string; noun: string }> = {
  shell: { tag: 'Shell', noun: 'commands' },
  json: { tag: 'JSON', noun: 'JSON' },
}

// Long lines wrap: a policy ARN or a shell flag cut at the right edge, behind
// a scrollbar the OS hides until you scroll, reads as the whole command. The
// Copy button still copies the text exactly as written. A block taller than
// the cap folds behind a fade and says how much is left, instead of cutting a
// line in half with nothing to show there is more.
function CodeBlock({ code, language, title }: { code: string; language?: string; title: string }) {
  const lang = LANGUAGE[language ?? ''] ?? { tag: 'Text', noun: 'text' }
  const pre = useRef<HTMLPreElement>(null)
  const [expanded, setExpanded] = useState(false)
  const [overflows, setOverflows] = useState(false)
  const lines = code.split('\n').length

  useLayoutEffect(() => {
    const el = pre.current
    if (!el || expanded) return
    const measure = () => setOverflows(el.scrollHeight > el.clientHeight + 1)
    measure()
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [code, expanded])

  return (
    <div className="rounded-lg border border-kb-border bg-kb-bg overflow-hidden">
      <div className="flex items-center justify-between px-2.5 py-1 border-b border-kb-border">
        <span className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary">{lang.tag}</span>
        <CopyButton value={code} name={`Copy the ${lang.noun} for “${title}”`} />
      </div>
      <div className="relative">
        <pre
          ref={pre}
          className={`px-3 py-2 text-[11px] font-mono text-kb-text-primary leading-snug whitespace-pre-wrap [overflow-wrap:anywhere] ${
            expanded ? '' : 'max-h-64 overflow-hidden'
          }`}
        >
          {code}
        </pre>
        {!expanded && overflows && (
          <div className="pointer-events-none absolute inset-x-0 bottom-0 h-12 bg-gradient-to-t from-kb-bg to-transparent" aria-hidden />
        )}
      </div>
      {(overflows || expanded) && (
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          aria-expanded={expanded}
          className="w-full px-3 py-1.5 border-t border-kb-border text-[11px] font-medium text-kb-text-secondary hover:text-kb-text-primary hover:bg-kb-card-hover transition-colors"
        >
          {expanded ? 'Show less' : `Show all ${lines} lines`}
        </button>
      )}
    </div>
  )
}
