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
    <div className="my-2 rounded-lg overflow-hidden border border-kb-border bg-[#0d1117] group relative">
      <div className="flex items-center justify-between px-3 py-1 border-b border-kb-border bg-kb-bg/40">
        <span className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-wider">
          {language || 'code'}
        </span>
        <button
          onClick={handleCopy}
          title={copied ? 'Copied!' : 'Copy to clipboard'}
          className="flex items-center gap-1 px-1.5 py-0.5 rounded text-[9px] font-mono text-kb-text-tertiary hover:text-kb-accent hover:bg-kb-elevated/40 transition-colors"
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
      <pre className="px-3 py-2 overflow-x-auto text-[11px] leading-relaxed">{children}</pre>
    </div>
  )
}

// Custom renderers that match KubeBolt's theme tokens.
const components: Components = {
  // Headings — compact but clearly differentiated
  h1: ({ children }) => (
    <h1 className="text-sm font-bold text-kb-text-primary mt-3 mb-2 first:mt-0">{children}</h1>
  ),
  h2: ({ children }) => (
    <h2 className="text-sm font-semibold text-kb-text-primary mt-3 mb-1.5 first:mt-0">{children}</h2>
  ),
  h3: ({ children }) => (
    <h3 className="text-[12px] font-semibold text-kb-text-primary mt-2.5 mb-1 first:mt-0">{children}</h3>
  ),
  h4: ({ children }) => (
    <h4 className="text-[11px] font-semibold text-kb-text-secondary mt-2 mb-1 first:mt-0 uppercase tracking-wider">{children}</h4>
  ),
  h5: ({ children }) => <h5 className="text-[11px] font-semibold text-kb-text-secondary mt-2 mb-1 first:mt-0">{children}</h5>,
  h6: ({ children }) => <h6 className="text-[11px] font-semibold text-kb-text-tertiary mt-2 mb-1 first:mt-0">{children}</h6>,

  // Paragraphs and inline text
  p: ({ children }) => (
    <p className="text-xs text-kb-text-primary leading-relaxed my-1.5 first:mt-0 last:mb-0">{children}</p>
  ),
  strong: ({ children }) => <strong className="font-semibold text-kb-text-primary">{children}</strong>,
  em: ({ children }) => <em className="italic text-kb-text-secondary">{children}</em>,
  del: ({ children }) => <del className="line-through text-kb-text-tertiary">{children}</del>,

  // Links — open in new tab, accent color
  a: ({ href, children }) => (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      className="text-kb-accent underline hover:opacity-80 transition-opacity"
    >
      {children}
    </a>
  ),

  // Lists
  ul: ({ children }) => (
    <ul className="list-disc list-outside ml-4 my-1.5 space-y-1 text-xs text-kb-text-primary">{children}</ul>
  ),
  ol: ({ children }) => (
    <ol className="list-decimal list-outside ml-4 my-1.5 space-y-1 text-xs text-kb-text-primary">{children}</ol>
  ),
  li: ({ children }) => <li className="leading-relaxed">{children}</li>,

  // Code element — react-markdown v10 no longer passes `inline`. Detect via
  // the presence of a language class (set only on fenced code blocks).
  code: ({ className, children, ...props }: any) => {
    const isBlock = /language-/.test(className || '')
    if (isBlock) {
      // Block code — let the pre component handle wrapping, just style content
      return (
        <code className="font-mono text-[#c9d1d9]" {...props}>
          {children}
        </code>
      )
    }
    // Inline code — small pill with subtle background
    return (
      <code
        className="px-1 py-0.5 rounded bg-kb-elevated text-[11px] font-mono text-kb-accent"
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
    <div className="my-2 overflow-x-auto rounded-lg border border-kb-border">
      <table className="w-full text-[11px] border-collapse">{children}</table>
    </div>
  ),
  thead: ({ children }) => <thead className="bg-kb-elevated/50">{children}</thead>,
  tbody: ({ children }) => <tbody>{children}</tbody>,
  tr: ({ children }) => <tr className="border-b border-kb-border last:border-0">{children}</tr>,
  th: ({ children }) => (
    <th className="px-2 py-1.5 text-left font-semibold text-kb-text-primary text-[10px] uppercase tracking-wider">
      {children}
    </th>
  ),
  td: ({ children }) => <td className="px-2 py-1.5 text-kb-text-primary align-top">{children}</td>,

  // Blockquote — subtle left border
  blockquote: ({ children }) => (
    <blockquote className="border-l-2 border-kb-accent/40 pl-3 my-2 text-xs text-kb-text-secondary italic">
      {children}
    </blockquote>
  ),

  // Horizontal rule
  hr: () => <hr className="my-3 border-kb-border" />,
}

interface MarkdownRendererProps {
  content: string
}

export function MarkdownRenderer({ content }: MarkdownRendererProps) {
  return (
    <div className="copilot-markdown">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
        {content}
      </ReactMarkdown>
    </div>
  )
}
