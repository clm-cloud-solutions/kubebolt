import { useState, type ReactNode } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { Check, Copy } from 'lucide-react'
import type { Components } from 'react-markdown'

// Extract plain text from React children — used to grab the raw code from
// the rendered <code> element so we can copy it to clipboard.
function extractText(node: ReactNode): string {
  if (node == null || node === false) return ''
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(extractText).join('')
  if (typeof node === 'object' && 'props' in (node as any)) {
    return extractText((node as any).props.children)
  }
  return ''
}

// ─── Lightweight fenced-block highlighting ──────────────────────────
//
// Full syntax trees are overkill for the two languages Kobi actually
// emits (kubectl walkthroughs and YAML patches), and a highlighter
// dependency would land on the bundle for a chat panel. Two line-based
// tokenizers cover the mockup's design language instead: comments dim,
// YAML keys in the accent green, unit-bearing quantities emphasized.
// Comment/value colors are FIXED, not theme tokens — the code surface
// stays dark in both themes, so theme text tokens would flip illegible
// in light mode.

const CODE_COMMENT_CLS = 'text-[#6b6864]'
const CODE_VALUE_CLS = 'font-semibold text-[#f0ede6]'

const BASH_LANGS = new Set(['bash', 'sh', 'shell', 'zsh', 'console'])
const YAML_LANGS = new Set(['yaml', 'yml'])

function highlightCode(lang: string, raw: string): ReactNode | null {
  const isBash = BASH_LANGS.has(lang)
  const isYaml = YAML_LANGS.has(lang)
  if (!isBash && !isYaml) return null
  const lines = raw.replace(/\n$/, '').split('\n')
  return lines.map((line, i) => {
    const suffix = i < lines.length - 1 ? '\n' : ''
    if (line.trimStart().startsWith('#')) {
      return (
        <span key={i} className={CODE_COMMENT_CLS}>
          {line + suffix}
        </span>
      )
    }
    // Trailing comments (`code  # note`) split off for both languages —
    // see splitTrailingComment for why the whitespace guard makes this safe.
    const [code, comment] = splitTrailingComment(line)
    const commentNode = comment ? <span className={CODE_COMMENT_CLS}>{comment}</span> : null
    if (isYaml) {
      // `key:` (optionally a list item) at any indent → accent key.
      // Nested flow-style keys ({ cpu: 100m }) stay plain, per the mockup.
      const m = /^(\s*(?:- )?)([A-Za-z0-9_./-]+)(:)(.*)$/.exec(code)
      if (m) {
        return (
          <span key={i}>
            {m[1]}
            <span className="text-kobi-accent">{m[2]}</span>
            {m[3]}
            {emphasizeQuantities(m[4])}
            {commentNode}
            {suffix}
          </span>
        )
      }
      return (
        <span key={i}>
          {emphasizeQuantities(code)}
          {commentNode}
          {suffix}
        </span>
      )
    }
    return (
      <span key={i}>
        {code}
        {commentNode}
        {suffix}
      </span>
    )
  })
}

// splitTrailingComment splits "code  # note" at the first # preceded by
// whitespace — the comment convention shared by YAML and shell. A # glued
// to text never opens a comment, which is exactly what keeps URL fragments
// (https://x.io/a#frag) and flag values (--sort-by=#…) undimmed. Known
// limit: a whitespace-# inside a quoted string would false-positive; Kobi's
// kubectl/YAML output doesn't produce those in practice.
function splitTrailingComment(line: string): [string, string | null] {
  const m = /\s#/.exec(line)
  if (!m) return [line, null]
  const hashAt = m.index + 1
  return [line.slice(0, hashAt), line.slice(hashAt)]
}

// Bold the unit-bearing quantities (512Mi, 100m, 80%…) in a YAML value —
// the numbers are what the operator is here to read.
function emphasizeQuantities(text: string): ReactNode {
  const parts = text.split(/(\b\d+(?:\.\d+)?(?:(?:Mi|Gi|Ki|Ti|m)\b|%))/g)
  if (parts.length === 1) return text
  return parts.map((p, i) =>
    i % 2 === 1 ? (
      <span key={i} className={CODE_VALUE_CLS}>
        {p}
      </span>
    ) : (
      p
    ),
  )
}

// CodeBlock — wraps a fenced code block with a header (language label) and a
// copy-to-clipboard button. Used by the pre component renderer below.
function CodeBlock({ language, children }: { language: string; children: ReactNode }) {
  const [copied, setCopied] = useState(false)

  function handleCopy() {
    const text = extractText(children).replace(/\n$/, '')
    navigator.clipboard.writeText(text).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    })
  }

  return (
    // Tokenized code surface (code stays dark in BOTH themes, per the
    // existing "code always has dark bg" convention) — the old hardcoded
    // GitHub-dark #0d1117 was a visual patch in light mode and off-palette.
    <div className="my-2 rounded-lg overflow-hidden border border-kobi-border bg-kobi-code-bg group relative max-w-full min-w-0">
      <div className="flex items-center justify-between px-3 py-1 border-b border-white/10 bg-white/[0.03]">
        <span className="text-[10px] font-mono text-kobi-text-tertiary uppercase tracking-wider">
          {language || 'code'}
        </span>
        <button
          onClick={handleCopy}
          title={copied ? 'Copied!' : 'Copy to clipboard'}
          className="flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-mono text-kobi-text-tertiary hover:text-kobi-accent hover:bg-white/10 transition-colors"
        >
          {copied ? (
            <>
              <Check className="w-3 h-3" />
              Copied
            </>
          ) : (
            <>
              <Copy className="w-3 h-3" />
              Copy
            </>
          )}
        </button>
      </div>
      <pre className="px-3 py-2 overflow-x-auto text-[13px] leading-relaxed max-w-full">{children}</pre>
    </div>
  )
}

// Custom renderers that match KubeBolt's theme tokens.
const components: Components = {
  // Headings — compact but clearly differentiated
  h1: ({ children }) => (
    <h1 className="text-[17px] font-bold tracking-tight text-kobi-text mt-3 mb-2 first:mt-0">{children}</h1>
  ),
  h2: ({ children }) => (
    <h2 className="text-[16px] font-semibold tracking-tight text-kobi-text mt-3 mb-1.5 first:mt-0">{children}</h2>
  ),
  h3: ({ children }) => (
    <h3 className="text-[15px] font-semibold text-kobi-text mt-2.5 mb-1 first:mt-0">{children}</h3>
  ),
  h4: ({ children }) => (
    <h4 className="text-xs font-semibold text-kobi-text-secondary mt-2 mb-1 first:mt-0 uppercase tracking-wider">{children}</h4>
  ),
  h5: ({ children }) => <h5 className="text-xs font-semibold text-kobi-text-secondary mt-2 mb-1 first:mt-0">{children}</h5>,
  h6: ({ children }) => <h6 className="text-xs font-semibold text-kobi-text-tertiary mt-2 mb-1 first:mt-0">{children}</h6>,

  // Paragraphs and inline text
  p: ({ children }) => (
    <p className="text-[15px] text-kobi-text leading-[1.65] my-1.5 first:mt-0 last:mb-0">{children}</p>
  ),
  strong: ({ children }) => <strong className="font-semibold text-kobi-text">{children}</strong>,
  em: ({ children }) => <em className="italic text-kobi-text-secondary">{children}</em>,
  del: ({ children }) => <del className="line-through text-kobi-text-tertiary">{children}</del>,

  // Links — open in new tab, accent color
  a: ({ href, children }) => (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      className="text-kobi-accent underline hover:opacity-80 transition-opacity"
    >
      {children}
    </a>
  ),

  // Lists
  ul: ({ children }) => (
    <ul className="list-disc list-outside ml-4 my-1.5 space-y-1 text-[15px] text-kobi-text">{children}</ul>
  ),
  ol: ({ children }) => (
    <ol className="list-decimal list-outside ml-4 my-1.5 space-y-1 text-[15px] text-kobi-text">{children}</ol>
  ),
  li: ({ children }) => <li className="leading-relaxed">{children}</li>,

  // Code element — react-markdown v10 no longer passes `inline`. Detect via
  // the presence of a language class (set only on fenced code blocks).
  code: ({ className, children, ...props }: any) => {
    const blockLang = /language-(\w+)/.exec(className || '')?.[1]
    if (blockLang || /language-/.test(className || '')) {
      // Block code — let the pre component handle wrapping, just style
      // content. yaml/bash get the lightweight token pass; other languages
      // render plain.
      const highlighted = blockLang ? highlightCode(blockLang, extractText(children)) : null
      return (
        <code className="font-mono text-kobi-code-text" {...props}>
          {highlighted ?? children}
        </code>
      )
    }
    // Inline code — small pill with subtle background
    return (
      <code
        className="px-1 py-0.5 rounded bg-kobi-elevated text-[13px] font-mono text-kobi-accent"
        {...props}
      >
        {children}
      </code>
    )
  },

  // Pre wraps fenced code blocks — delegated to CodeBlock for copy support.
  pre: ({ children }: any) => {
    const codeProps = (children as any)?.props || {}
    const language = /language-(\w+)/.exec(codeProps.className || '')?.[1] || ''
    return <CodeBlock language={language}>{children}</CodeBlock>
  },

  // Tables — proper rendering with borders
  table: ({ children }) => (
    <div className="my-2 overflow-x-auto rounded-lg border border-kobi-border">
      <table className="w-full text-xs border-collapse">{children}</table>
    </div>
  ),
  thead: ({ children }) => <thead className="bg-kobi-elevated/50">{children}</thead>,
  tbody: ({ children }) => <tbody>{children}</tbody>,
  tr: ({ children }) => <tr className="border-b border-kobi-border last:border-0">{children}</tr>,
  // th — secondary color per the mockup: the header differentiates from the
  // body rows by being DIMMER (plus the thead tint), not louder. It was
  // primary text before, identical to the cells at a glance.
  th: ({ children }) => (
    <th className="px-2 py-1.5 text-left font-semibold text-kobi-text-secondary text-[11px]">
      {children}
    </th>
  ),
  td: ({ children }) => <td className="px-2 py-1.5 text-kobi-text align-top">{children}</td>,

  // Blockquote — subtle left border
  blockquote: ({ children }) => (
    <blockquote className="border-l-2 border-kobi-accent/40 pl-3 my-2 text-[15px] text-kobi-text-secondary italic">
      {children}
    </blockquote>
  ),

  // Horizontal rule
  hr: () => <hr className="my-3 border-kobi-border" />,
}

interface MarkdownRendererProps {
  content: string
}

export function MarkdownRenderer({ content }: MarkdownRendererProps) {
  return (
    <div className="copilot-markdown min-w-0 max-w-full w-full [overflow-wrap:anywhere]">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
        {content}
      </ReactMarkdown>
    </div>
  )
}
